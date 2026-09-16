package orch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/datagen"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/hypervisor"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

const gib = int64(1) << 30

// fillPollInterval is how often the fill job checks per-VM progress.
var fillPollInterval = 5 * time.Second

// fillTimeout caps a whole fill run; large fleets writing real data can take
// a long time, so this is generous.
const fillTimeout = 12 * time.Hour

// agentSilenceLimit fails a run if a working VM's agent stops heartbeating
// for this long (it heartbeats every 10s) — surfaces a crashed/hung agent
// promptly instead of waiting out fillTimeout.
const agentSilenceLimit = 90 * time.Second

// fillStallLimit fails a run that makes no forward progress for this long: no
// VM is actively writing and none have written new bytes. Catches a fleet that
// never boots/registers (e.g. killed VMs) without waiting out fillTimeout.
// Generous because boot + mkfs can precede the first byte.
var fillStallLimit = 10 * time.Minute

// agentFreshLimit is how recently an agent must have heartbeated (10 s cadence)
// to be trusted as alive when a fill starts. A live agent picks the new work
// order up on its next heartbeat, so its VM must not be power-cycled; anything
// staler is in an unknown state and gets a fresh boot.
var agentFreshLimit = 30 * time.Second

// bootTimeout is how long a pending VM may go after (re)power-on without its
// agent checking in before the boot is declared wedged and the VM is
// power-cycled. PXE boot normally takes ~1-2 min; a VM stuck in the EFI/iPXE
// stage never recovers without a hard reset (the Jun 2026 incident: one VM's
// PXE boot failed once and it sat wedged-powered-on for ten days of nightly
// runs, because "power on" is a no-op on a powered-on VM).
var bootTimeout = 5 * time.Minute

// maxBootAttempts caps boot attempts (initial power-on plus resets) per VM per
// run. After that the VM alone is marked failed, so one unbootable VM degrades
// the run to a partial result instead of stalling the whole fleet.
var maxBootAttempts = 3

// StartFill launches an initial-fill, incremental or verify run: it assigns
// every VM a work order target, powers the VMs on (so they PXE-boot the
// agent), then waits for the agents to report completion. Agents fetch their
// actual work order via WorkOrderFor on register/heartbeat.
func (o *Orchestrator) StartFill(d *model.Deployment, runType string) (*model.Run, error) {
	ctx, err := o.acquire(d.ID)
	if err != nil {
		return nil, err
	}
	vms, err := o.store.ListManagedVMs(d.ID)
	if err != nil {
		o.release(d.ID)
		return nil, err
	}
	if len(vms) == 0 {
		o.release(d.ID)
		return nil, ErrPrecondition{"nothing to fill: deploy the VMs first"}
	}
	// Incremental and verify need existing data: a succeeded initial fill —
	// or an adopted deployment, whose VMs arrived with their data (restored
	// disks) instead of being filled here.
	if runType == model.RunIncremental || runType == model.RunVerify {
		filled, err := o.store.HasSucceededRun(d.ID, model.RunInitialFill)
		if err != nil {
			o.release(d.ID)
			return nil, err
		}
		if !filled && d.Origin != model.OriginAdopted {
			o.release(d.ID)
			return nil, ErrPrecondition{fmt.Sprintf("run an initial fill before a %s", runType)}
		}
	}
	run, err := o.store.CreateRun(d.ID, runType)
	if err != nil {
		o.release(d.ID)
		return nil, err
	}
	total := bytesPerVM(d.Spec, runType)
	for _, vm := range vms {
		o.store.AssignFill(vm.ID, run.ID, total)
	}
	o.store.StartRun(run.ID)
	go o.runFill(ctx, d, run, vms)
	return run, nil
}

// bytesPerVM is the payload one VM writes in a run: full data for a fill,
// or the changed+grown fraction for an incremental. Verify reads the full
// data back, so it uses the fill estimate too (growth from incrementals can
// push the actual amount slightly above it — the agent reports actuals).
func bytesPerVM(spec model.ProfileSpec, runType string) int64 {
	data := int64(spec.DisksPerVM) * int64(spec.DataPerDiskGiB) * gib
	if runType == model.RunIncremental {
		frac := (spec.ChangePercent + spec.GrowthPercent) / 100.0
		return int64(float64(data) * frac)
	}
	return data
}

func (o *Orchestrator) runFill(ctx context.Context, d *model.Deployment, run *model.Run, vms []*model.ManagedVM) {
	defer o.release(d.ID)
	start := time.Now()

	driver, err := o.OpenDriver(ctx, d.ConnectionID)
	if err != nil {
		o.failFill(run, fmt.Errorf("opening hypervisor connection: %w", err))
		return
	}
	defer driver.Close()

	// Boot the fleet, deciding per VM from the hypervisor's actual power
	// state — never from the agent heartbeat alone. A powered-off VM is simply
	// powered on. A powered-on VM whose agent heartbeated within
	// agentFreshLimit is left untouched: the live agent picks the work order
	// up on its next heartbeat. A powered-on VM without a live agent is
	// unreachable any other way, so it gets a hard power-cycle for a fresh
	// PXE boot. The power-state check matters because heartbeats stay fresh
	// for up to 30 s after the previous run's post-fill shutdown; trusting
	// them alone left a fleet powered off with a "running" incremental until
	// the boot watchdog kicked in.
	bootAt := make(map[string]time.Time, len(vms))
	bootTries := make(map[string]int, len(vms))
	for _, vm := range vms {
		bootAt[vm.ID], bootTries[vm.ID] = start, 1
		o.bootVM(ctx, driver, vm)
	}

	// writeStart marks when data first started flowing (after the VMs boot
	// the agent), so throughput reflects the write phase, not the boot wait.
	var writeStart time.Time

	// Stall detection: remember the last poll where the run moved forward.
	lastProgress := start
	var prevWritten int64

	deadline := start.Add(fillTimeout)
	for {
		select {
		case <-ctx.Done():
			o.cancelFill(run, d)
			return
		case <-time.After(fillPollInterval):
		}
		cur, err := o.store.ListManagedVMs(d.ID)
		if err != nil {
			o.failFill(run, err)
			return
		}
		done, failed, working := 0, 0, 0
		var bytesWritten, bytesTotal int64
		var aggMBps float64
		var silent string // a working VM whose agent has gone quiet
		var pending []*model.ManagedVM
		for _, vm := range cur {
			if vm.FillRunID != run.ID {
				continue
			}
			bytesWritten += vm.BytesWritten
			bytesTotal += vm.BytesTotal
			switch vm.FillStatus {
			case model.FillDone:
				done++
			case model.FillFailed:
				failed++
			case model.FillPending:
				pending = append(pending, vm)
			case model.FillWorking:
				working++
				aggMBps += vm.MBps
				if vm.AgentSeenAt != nil && time.Since(*vm.AgentSeenAt) > agentSilenceLimit {
					silent = fmt.Sprintf("%s (silent %ds, last at %.1f GiB)",
						vm.Name, int(time.Since(*vm.AgentSeenAt).Seconds()), float64(vm.BytesWritten)/(1<<30))
				}
			}
		}
		// Boot watchdog: a pending VM whose agent hasn't checked in since its
		// last (re)boot is wedged somewhere before registration (EFI, PXE,
		// kernel). Power-cycle it for another attempt; after maxBootAttempts
		// give up on that VM alone so the fleet's result doesn't hinge on one
		// flaky boot.
		for _, vm := range pending {
			if vm.AgentSeenAt != nil && vm.AgentSeenAt.After(bootAt[vm.ID]) {
				continue // alive since its boot; e.g. waiting for a MaxParallelVMs slot
			}
			if time.Since(bootAt[vm.ID]) < bootTimeout {
				continue
			}
			if bootTries[vm.ID] >= maxBootAttempts {
				slog.Warn("fill: giving up on VM, agent never registered", "vm", vm.Name, "attempts", bootTries[vm.ID])
				if err := o.store.FailFillVM(vm.ID,
					fmt.Sprintf("agent never registered after %d boot attempts — check the VM console", bootTries[vm.ID])); err != nil {
					slog.Warn("fill: marking VM failed", "vm", vm.Name, "err", err)
				}
				continue
			}
			slog.Warn("fill: agent not registered in time, power-cycling VM",
				"vm", vm.Name, "attempt", bootTries[vm.ID]+1, "of", maxBootAttempts)
			o.powerCycle(ctx, driver, vm)
			bootAt[vm.ID], bootTries[vm.ID] = time.Now(), bootTries[vm.ID]+1
			lastProgress = time.Now() // a fresh boot restarts the stall clock
		}
		// A working VM that stopped heartbeating means its agent crashed or
		// the VM hung — fail fast rather than wait out the 12h timeout.
		if silent != "" && time.Since(start) > agentSilenceLimit {
			o.failFillWithStats(run, fmt.Errorf("agent went silent: %s — check the VM's console.log on the datastore", silent),
				done, failed, len(vms), bytesWritten, time.Since(start))
			o.applyAfterFill(ctx, driver, d, cur)
			return
		}
		// No VM working and no new bytes for fillStallLimit → the fleet never
		// booted/registered (or all agents are gone); fail instead of waiting
		// out fillTimeout.
		if working > 0 || bytesWritten > prevWritten {
			lastProgress = time.Now()
		}
		prevWritten = bytesWritten
		if time.Since(lastProgress) > fillStallLimit {
			err := fmt.Errorf("fill stalled: no VM progressed for %s — check VM boot / agent registration on the console", fillStallLimit)
			if len(pending) > 0 {
				err = fmt.Errorf("fill stalled: no VM progressed for %s; never started: %s", fillStallLimit, vmNames(pending))
			}
			o.failFillWithStats(run, err, done, failed, len(vms), bytesWritten, time.Since(start))
			o.applyAfterFill(ctx, driver, d, cur)
			return
		}
		if bytesWritten > 0 && writeStart.IsZero() {
			writeStart = time.Now()
		}
		o.publishFillStats(run, false, done, failed, len(vms), bytesWritten, bytesTotal, aggMBps, time.Since(start))
		o.store.AddRunSample(run.ID, round1(aggMBps), bytesWritten)

		if done+failed >= len(vms) {
			writeDur := time.Since(start)
			if !writeStart.IsZero() {
				writeDur = time.Since(writeStart)
			}
			o.finishFill(ctx, driver, d, run, cur, done, failed, bytesWritten, writeDur)
			return
		}
		if time.Now().After(deadline) {
			o.failFillWithStats(run, fmt.Errorf("fill timed out after %s (%d/%d VMs done)", fillTimeout, done, len(vms)),
				done, failed, len(vms), bytesWritten, time.Since(start))
			o.applyAfterFill(ctx, driver, d, cur)
			return
		}
	}
}

// bootVM brings a VM into a state where its agent will pick up the run's work
// order: powers it on if it is off, leaves it alone if it is on with a live
// agent, and power-cycles it if it is on without one.
func (o *Orchestrator) bootVM(ctx context.Context, driver hypervisor.Driver, vm *model.ManagedVM) {
	v, err := driver.GetVM(ctx, vm.Ref)
	if err != nil {
		slog.Warn("fill: reading VM power state failed, power-cycling", "vm", vm.Name, "err", err)
		o.powerCycle(ctx, driver, vm)
		return
	}
	if v != nil && v.State == hypervisor.StatePoweredOn {
		if vm.AgentSeenAt != nil && time.Since(*vm.AgentSeenAt) < agentFreshLimit {
			return // live agent; it picks up the work order on its next heartbeat
		}
		o.powerCycle(ctx, driver, vm)
		return
	}
	if err := driver.PowerOn(ctx, vm.Ref); err != nil {
		slog.Warn("fill: power on failed", "vm", vm.Name, "err", err)
	}
}

// powerCycle forces a fresh PXE boot: hard off if the VM is running (tolerated
// if it is gone or races off), then on. The temp OS is stateless and every
// write is deterministic/idempotent, so a hard cycle is always safe.
func (o *Orchestrator) powerCycle(ctx context.Context, driver hypervisor.Driver, vm *model.ManagedVM) {
	if v, err := driver.GetVM(ctx, vm.Ref); err == nil && v != nil && v.State == hypervisor.StatePoweredOn {
		if err := driver.PowerOff(ctx, vm.Ref); err != nil {
			slog.Warn("fill: power off for reset failed", "vm", vm.Name, "err", err)
		}
	}
	if err := driver.PowerOn(ctx, vm.Ref); err != nil {
		slog.Warn("fill: power on failed", "vm", vm.Name, "err", err)
	}
}

// applyAfterFill leaves the fleet in the configured post-run power state. It
// runs on failure too, not just success: a fleet left powered on after a
// failed run re-wedges every following run (a VM stuck in PXE stays stuck
// until a power cycle), so a known powered-off state beats keeping a broken
// boot around for inspection — the console log survives the power-off.
func (o *Orchestrator) applyAfterFill(ctx context.Context, driver hypervisor.Driver, d *model.Deployment, vms []*model.ManagedVM) {
	if d.Spec.AfterFill != model.AfterFillShutdown {
		return
	}
	for _, vm := range vms {
		if err := driver.PowerOff(ctx, vm.Ref); err != nil {
			slog.Warn("fill: post-run power off failed", "vm", vm.Name, "err", err)
			continue
		}
		// The agent died with the power; drop its liveness so neither the UI
		// nor the next run's boot pass mistakes a fresh heartbeat for a live VM.
		if err := o.store.ResetAgent(vm.ID); err != nil {
			slog.Warn("fill: resetting agent state after power off", "vm", vm.Name, "err", err)
		}
	}
}

// vmNames joins VM names for error messages.
func vmNames(vms []*model.ManagedVM) string {
	names := make([]string, len(vms))
	for i, vm := range vms {
		names[i] = vm.Name
	}
	return strings.Join(names, ", ")
}

// finishFill records the terminal run result and applies the post-fill power
// action (PRO-5) — on failure too, so the fleet always ends in a known state.
// vms is the freshly-listed fleet, so per-VM fill errors name the culprits.
func (o *Orchestrator) finishFill(ctx context.Context, driver hypervisor.Driver, d *model.Deployment, run *model.Run, vms []*model.ManagedVM, done, failed int, bytesWritten int64, dur time.Duration) {
	if failed > 0 {
		var why []string
		for _, vm := range vms {
			if vm.FillRunID == run.ID && vm.FillStatus == model.FillFailed {
				why = append(why, fmt.Sprintf("%s: %s", vm.Name, vm.FillError))
			}
		}
		o.store.FinishRun(run.ID, model.RunFailed,
			mustJSON(map[string]any{"error": fmt.Sprintf("%d/%d VMs failed — %s", failed, len(vms), strings.Join(why, "; ")),
				"vmsDone": done, "vmsFailed": failed, "bytesWritten": bytesWritten, "durationSec": round1(dur.Seconds())}))
		o.applyAfterFill(ctx, driver, d, vms)
		return
	}
	o.applyAfterFill(ctx, driver, d, vms)
	mbAvg := 0.0
	if dur.Seconds() > 0 {
		mbAvg = float64(bytesWritten) / (1024 * 1024) / dur.Seconds()
	}
	o.store.FinishRun(run.ID, model.RunSucceeded, mustJSON(map[string]any{
		"vmsDone": done, "bytesWritten": bytesWritten,
		"avgMBps": round1(mbAvg), "durationSec": round1(dur.Seconds()),
	}))
}

func (o *Orchestrator) failFill(run *model.Run, err error) {
	slog.Error("fill failed", "run", run.Type, "err", err)
	o.store.FinishRun(run.ID, model.RunFailed, mustJSON(map[string]any{"error": err.Error()}))
}

// failFillWithStats is failFill for mid-run failures: it keeps what the run
// did achieve (VMs done, bytes written) next to the error, instead of
// discarding a 90%-complete run's progress behind a bare error string.
func (o *Orchestrator) failFillWithStats(run *model.Run, err error, done, failed, total int, bytesWritten int64, dur time.Duration) {
	slog.Error("fill failed", "run", run.Type, "err", err)
	o.store.FinishRun(run.ID, model.RunFailed, mustJSON(map[string]any{
		"error": err.Error(), "vmsDone": done, "vmsFailed": failed, "vmsTotal": total,
		"bytesWritten": bytesWritten, "durationSec": round1(dur.Seconds()),
	}))
}

// cancelFill records a user-aborted fill run. The run is no longer running, so
// WorkOrderForToken hands every agent "idle" on its next heartbeat; resetting
// the still-unfinished VMs stops the UI from showing them in flight.
func (o *Orchestrator) cancelFill(run *model.Run, d *model.Deployment) {
	slog.Info("fill cancelled", "deployment", d.Name, "run", run.Type)
	if err := o.store.CancelUnfinishedFills(run.ID); err != nil {
		slog.Warn("fill cancel: resetting VMs", "err", err)
	}
	o.store.FinishRun(run.ID, model.RunCancelled, mustJSON(map[string]any{"error": "cancelled by user"}))
}

// publishFillStats writes a live progress snapshot to the run's stats so the
// UI sees aggregate throughput during the run (final=false) and at the end.
func (o *Orchestrator) publishFillStats(run *model.Run, final bool, done, failed, total int, written, totalBytes int64, mbps float64, dur time.Duration) {
	if final {
		return // finishFill writes the terminal stats
	}
	o.store.SetRunStats(run.ID, mustJSON(map[string]any{
		"vmsDone": done, "vmsFailed": failed, "vmsTotal": total,
		"bytesWritten": written, "bytesTotal": totalBytes,
		"aggMBps": round1(mbps), "durationSec": round1(dur.Seconds()),
	}))
}

// WorkOrderForToken resolves the agent's current instruction. Returns
// action "fill" with a work order while a run is active for the VM, else
// "idle". Promotes a pending VM to working, honoring MaxParallelVMs.
func (o *Orchestrator) WorkOrderForToken(vm *model.ManagedVM) (string, *datagen.WorkOrder) {
	if vm.FillRunID == "" || vm.FillStatus == model.FillDone || vm.FillStatus == model.FillFailed {
		return "idle", nil
	}
	run, err := o.store.GetRun(vm.FillRunID)
	if err != nil || run.Status != model.RunRunning {
		return "idle", nil
	}
	d, err := o.store.GetDeployment(vm.DeploymentID)
	if err != nil {
		return "idle", nil
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	if vm.FillStatus == model.FillPending {
		// Concurrency gate: don't exceed MaxParallelVMs working at once.
		if cap := d.Spec.MaxParallelVMs; cap > 0 {
			working, _ := o.store.CountFillStatus(run.ID, model.FillWorking)
			if working >= cap {
				return "idle", nil // wait for a slot
			}
		}
		o.store.SetFillStatus(vm.ID, model.FillWorking)
	}

	wo := datagen.BuildWorkOrder(d.ID, d.Name, run.ID, run.Type, vm.Name, d.Spec)
	wo.RateLimitKBps = perVMRateKBps(d.Spec)
	return "fill", &wo
}

// perVMRateKBps splits the deployment's aggregate cap across the VMs that
// run concurrently (static per-wave split; 0 = unlimited).
func perVMRateKBps(spec model.ProfileSpec) int {
	if spec.RateLimitMBps <= 0 {
		return 0
	}
	par := spec.VMCount
	if spec.MaxParallelVMs > 0 && spec.MaxParallelVMs < par {
		par = spec.MaxParallelVMs
	}
	if par < 1 {
		par = 1
	}
	return spec.RateLimitMBps * 1024 / par
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func round1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
