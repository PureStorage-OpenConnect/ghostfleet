// Package orch reconciles deployments against the hypervisor: it computes
// the diff between a deployment's effective config (desired) and its managed
// VMs (actual), then creates VMs, adds disks and extends disks to close the
// gap. The diff-based approach makes every job retryable (NFR-3) and gives
// scale-up for free (PRO-6): grow the config, reconcile again.
package orch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/hypervisor"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/secrets"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/store"
)

// createParallelism bounds concurrent VM creations per job, so a large
// deployment doesn't flood vCenter with hundreds of parallel tasks.
const createParallelism = 4

// Orchestrator runs deploy/teardown jobs asynchronously, one at a time per
// deployment.
type Orchestrator struct {
	store    *store.Store
	secrets  *secrets.Box
	registry hypervisor.Registry

	mu     sync.Mutex
	active map[string]context.CancelFunc // deployment ID -> cancel for its running job
	jobs   sync.WaitGroup                // one count per active entry, released with it
}

// New wires an orchestrator.
func New(st *store.Store, box *secrets.Box, registry hypervisor.Registry) *Orchestrator {
	return &Orchestrator{store: st, secrets: box, registry: registry, active: make(map[string]context.CancelFunc)}
}

// OpenDriver opens a hypervisor driver for a stored connection. Also used
// by the API for connection validation and placement enumeration.
func (o *Orchestrator) OpenDriver(ctx context.Context, connectionID string) (hypervisor.Driver, error) {
	conn, err := o.store.GetConnection(connectionID)
	if err != nil {
		return nil, err
	}
	blob, err := o.store.GetConnectionSecret(connectionID)
	if err != nil {
		return nil, err
	}
	secret, err := o.secrets.Decrypt(blob)
	if err != nil {
		return nil, err
	}
	return o.registry.Open(ctx, conn, secret)
}

// ErrBusy is returned when a job is already running for the deployment.
type ErrBusy struct{ DeploymentID string }

func (e ErrBusy) Error() string { return "a job is already running for this deployment" }

// ErrNotRunning is returned when asked to cancel a deployment with no active job.
type ErrNotRunning struct{ DeploymentID string }

func (e ErrNotRunning) Error() string { return "no job is running for this deployment" }

// ErrPrecondition is a user-correctable reason a job can't start (e.g. an
// incremental before any initial fill). The API surfaces its message as 409.
type ErrPrecondition struct{ Reason string }

func (e ErrPrecondition) Error() string { return e.Reason }

// acquire marks the deployment busy and returns a cancellable context for its
// job, or ErrBusy if one is already running. Cancel/release end the job.
func (o *Orchestrator) acquire(deploymentID string) (context.Context, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.active[deploymentID]; ok {
		return nil, ErrBusy{DeploymentID: deploymentID}
	}
	ctx, cancel := context.WithCancel(context.Background())
	o.active[deploymentID] = cancel
	o.jobs.Add(1)
	return ctx, nil
}

func (o *Orchestrator) release(deploymentID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if cancel, ok := o.active[deploymentID]; ok {
		cancel() // free the context; harmless if already cancelled
		delete(o.active, deploymentID)
		o.jobs.Done()
	}
}

// Stop cancels every running job and blocks until each has returned. The
// jobs record themselves as cancelled, so this is for tests and tooling that
// need a quiescent orchestrator. The controller deliberately does not call
// it on shutdown: an interrupted job stays "running" in the store and
// ResumeInterrupted re-attaches to it on the next start (NFR-2/NFR-3).
func (o *Orchestrator) Stop() {
	o.mu.Lock()
	for _, cancel := range o.active {
		cancel()
	}
	o.mu.Unlock()
	o.jobs.Wait()
}

// Cancel aborts the deployment's running job, if any. The job's context is
// cancelled; it then records the run as cancelled and returns.
func (o *Orchestrator) Cancel(deploymentID string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	cancel, ok := o.active[deploymentID]
	if !ok {
		return ErrNotRunning{DeploymentID: deploymentID}
	}
	cancel()
	return nil
}

// IsActive reports whether a job is currently running for the deployment.
func (o *Orchestrator) IsActive(deploymentID string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	_, ok := o.active[deploymentID]
	return ok
}

// StartDeploy launches an async reconcile job (deploy or scale-up) and
// returns the created run. onConflict decides what happens if a target VM name
// already exists owned by another/unknown deployment (model.OnConflict*).
func (o *Orchestrator) StartDeploy(d *model.Deployment, onConflict string) (*model.Run, error) {
	if d.Origin == model.OriginAdopted {
		// Adopted deployments own VMs the controller never created; there is
		// no desired fleet shape to reconcile toward (its VMs joined via
		// adoption). Fill/incremental/verify/power/teardown all apply.
		return nil, ErrPrecondition{"adopted deployments cannot be deployed or scaled: their VMs join via adoption"}
	}
	ctx, err := o.acquire(d.ID)
	if err != nil {
		return nil, err
	}
	existing, err := o.store.ListManagedVMs(d.ID)
	if err != nil {
		o.release(d.ID)
		return nil, err
	}
	runType := model.RunDeploy
	if len(existing) > 0 {
		runType = model.RunScaleUp
	}
	run, err := o.store.CreateRun(d.ID, runType)
	if err != nil {
		o.release(d.ID)
		return nil, err
	}
	o.store.SetDeploymentStatus(d.ID, model.DeploymentDeploying)
	go o.runJob(ctx, d, run, o.reconcileFunc(onConflict))
	return run, nil
}

// StartTeardown launches an async teardown job and returns the created run.
func (o *Orchestrator) StartTeardown(d *model.Deployment) (*model.Run, error) {
	ctx, err := o.acquire(d.ID)
	if err != nil {
		return nil, err
	}
	run, err := o.store.CreateRun(d.ID, model.RunTeardown)
	if err != nil {
		o.release(d.ID)
		return nil, err
	}
	o.store.SetDeploymentStatus(d.ID, model.DeploymentDeleting)
	go o.runJob(ctx, d, run, o.teardown)
	return run, nil
}

// StartPower launches an async job powering every managed VM on or off.
// The deployment status is not changed — power state is orthogonal.
func (o *Orchestrator) StartPower(d *model.Deployment, on bool) (*model.Run, error) {
	ctx, err := o.acquire(d.ID)
	if err != nil {
		return nil, err
	}
	runType := model.RunPowerOff
	if on {
		runType = model.RunPowerOn
	}
	run, err := o.store.CreateRun(d.ID, runType)
	if err != nil {
		o.release(d.ID)
		return nil, err
	}
	go o.runJob(ctx, d, run, o.powerFunc(on))
	return run, nil
}

// powerFunc builds the job that powers every managed VM on or off.
func (o *Orchestrator) powerFunc(on bool) jobFunc {
	return func(ctx context.Context, d *model.Deployment, driver hypervisor.Driver, stats *jobStats) error {
		vms, err := o.store.ListManagedVMs(d.ID)
		if err != nil {
			return err
		}
		for _, vm := range vms {
			op := driver.PowerOff
			if on {
				op = driver.PowerOn
			}
			if err := op(ctx, vm.Ref); err != nil {
				return fmt.Errorf("power VM %s: %w", vm.Name, err)
			}
			stats.VMsPowered++
		}
		return nil
	}
}

// ResumeInterrupted re-attaches jobs left running by a controller restart
// (NFR-2/NFR-3): each run still marked running gets its goroutine relaunched
// against the existing run record. Reconcile/teardown/power are idempotent;
// fills re-attach their monitor (per-VM progress lives in the DB and agents
// keep heartbeating). Called once at startup before serving.
func (o *Orchestrator) ResumeInterrupted() {
	runs, err := o.store.ListRunningRuns()
	if err != nil {
		slog.Error("resume: listing running runs", "err", err)
		return
	}
	for _, run := range runs {
		d, err := o.store.GetDeployment(run.DeploymentID)
		if err != nil {
			slog.Warn("resume: deployment gone, failing orphaned run", "run", run.ID, "err", err)
			o.store.FinishRun(run.ID, model.RunFailed, mustJSON(map[string]any{"error": "interrupted (controller restart); deployment missing"}))
			continue
		}
		ctx, err := o.acquire(d.ID)
		if err != nil {
			continue // already active (shouldn't happen at startup)
		}
		slog.Info("resuming interrupted run", "deployment", d.Name, "type", run.Type, "run", run.ID)
		switch run.Type {
		case model.RunDeploy, model.RunScaleUp:
			go o.runJob(ctx, d, run, o.reconcile)
		case model.RunTeardown:
			go o.runJob(ctx, d, run, o.teardown)
		case model.RunPowerOn:
			go o.runJob(ctx, d, run, o.powerFunc(true))
		case model.RunPowerOff:
			go o.runJob(ctx, d, run, o.powerFunc(false))
		case model.RunInitialFill, model.RunIncremental, model.RunVerify:
			vms, err := o.store.ListManagedVMs(d.ID)
			if err != nil {
				o.release(d.ID)
				o.failFill(run, fmt.Errorf("resume: listing VMs: %w", err))
				continue
			}
			go o.runFill(ctx, d, run, vms)
		default:
			o.release(d.ID)
			o.store.FinishRun(run.ID, model.RunFailed, mustJSON(map[string]any{"error": "interrupted (controller restart); unknown run type"}))
		}
	}
}

// jobStats summarizes what a job did; stored as the run's stats JSON.
type jobStats struct {
	VMsCreated    int     `json:"vmsCreated,omitempty"`
	DisksAdded    int     `json:"disksAdded,omitempty"`
	DisksExtended int     `json:"disksExtended,omitempty"`
	VMsDeleted    int     `json:"vmsDeleted,omitempty"`
	VMsPowered    int     `json:"vmsPowered,omitempty"`
	DurationSec   float64 `json:"durationSec"`
}

type jobFunc func(ctx context.Context, d *model.Deployment, driver hypervisor.Driver, stats *jobStats) error

// runJob executes a job with driver setup/teardown and run bookkeeping.
func (o *Orchestrator) runJob(ctx context.Context, d *model.Deployment, run *model.Run, fn jobFunc) {
	defer o.release(d.ID)
	start := time.Now()
	stats := jobStats{}
	o.store.StartRun(run.ID)

	// Power jobs never change the deployment status — power state is
	// orthogonal to whether the fleet matches its effective config.
	isPower := run.Type == model.RunPowerOn || run.Type == model.RunPowerOff

	fail := func(err error) {
		stats.DurationSec = time.Since(start).Seconds()
		// A cancelled context means the user aborted the job; record it as
		// cancelled (not failed) but still flag the deployment as needing
		// attention, since the fleet may be left half-reconciled.
		if ctx.Err() != nil {
			slog.Info("job cancelled", "deployment", d.Name, "run", run.Type)
			statsJSON, _ := json.Marshal(map[string]any{"error": "cancelled by user", "partial": stats})
			o.store.FinishRun(run.ID, model.RunCancelled, string(statsJSON))
			if !isPower {
				o.store.SetDeploymentStatus(d.ID, model.DeploymentError)
			}
			return
		}
		slog.Error("job failed", "deployment", d.Name, "run", run.Type, "err", err)
		statsJSON, _ := json.Marshal(map[string]any{"error": err.Error(), "partial": stats})
		o.store.FinishRun(run.ID, model.RunFailed, string(statsJSON))
		if !isPower {
			o.store.SetDeploymentStatus(d.ID, model.DeploymentError)
		}
	}

	driver, err := o.OpenDriver(ctx, d.ConnectionID)
	if err != nil {
		fail(fmt.Errorf("opening hypervisor connection: %w", err))
		return
	}
	defer driver.Close()

	if err := fn(ctx, d, driver, &stats); err != nil {
		fail(err)
		return
	}

	stats.DurationSec = time.Since(start).Seconds()
	statsJSON, _ := json.Marshal(stats)
	o.store.FinishRun(run.ID, model.RunSucceeded, string(statsJSON))
	if !isPower {
		if run.Type == model.RunTeardown {
			o.store.MarkDeploymentDeleted(d.ID) // also frees the name for reuse
		} else {
			o.store.SetDeploymentStatus(d.ID, model.DeploymentReady)
		}
	}
	slog.Info("job finished", "deployment", d.Name, "run", run.Type,
		"durationSec", int(stats.DurationSec), "stats", string(statsJSON))
}

// reconcile closes the gap between effective config and managed VMs, aborting
// on a cross-deployment conflict. Used by ResumeInterrupted (a resumed deploy
// has no foreign conflicts left — they were resolved on the first attempt).
func (o *Orchestrator) reconcile(ctx context.Context, d *model.Deployment, driver hypervisor.Driver, stats *jobStats) error {
	return o.reconcileCore(ctx, d, driver, stats, model.OnConflictAbort)
}

// reconcileFunc binds a conflict-resolution mode for StartDeploy.
func (o *Orchestrator) reconcileFunc(onConflict string) jobFunc {
	return func(ctx context.Context, d *model.Deployment, driver hypervisor.Driver, stats *jobStats) error {
		return o.reconcileCore(ctx, d, driver, stats, onConflict)
	}
}

// reconcileCore closes the gap between effective config and managed VMs. It
// first resolves cross-deployment conflicts (target names already on the
// hypervisor owned by another/unknown deployment) per onConflict: abort (fail),
// clean (delete them, then create fresh) or adopt (CreateVM takes them over and
// re-stamps ownership).
func (o *Orchestrator) reconcileCore(ctx context.Context, d *model.Deployment, driver hypervisor.Driver, stats *jobStats, onConflict string) error {
	spec := d.Spec
	actual, err := o.store.ListManagedVMs(d.ID)
	if err != nil {
		return err
	}
	byName := make(map[string]*model.ManagedVM, len(actual))
	for _, vm := range actual {
		byName[vm.Name] = vm
	}

	conflicts, err := o.findConflicts(ctx, d, driver, byName)
	if err != nil {
		return fmt.Errorf("checking for conflicting VMs: %w", err)
	}
	if len(conflicts) > 0 {
		switch onConflict {
		case model.OnConflictClean:
			for _, c := range conflicts {
				if err := driver.DeleteVM(ctx, c.Ref); err != nil {
					return fmt.Errorf("cleaning conflicting VM %s: %w", c.Name, err)
				}
				stats.VMsDeleted++
			}
		case model.OnConflictAdopt:
			// CreateVM adopts the existing VM and re-stamps ownership.
		default: // abort
			return fmt.Errorf("%d VM name(s) already exist owned by another deployment (%s); deploy with adopt or clean to resolve",
				len(conflicts), conflictSummary(conflicts))
		}
	}

	// 1. Create missing VMs (bounded parallelism). Indices are 1-based and
	// stable, so round-robin datastore assignment stays consistent across
	// scale-ups (a new VM keeps the slot its index maps to).
	var missing []int
	for i := 1; i <= spec.VMCount; i++ {
		if name := spec.VMName(i); byName[name] == nil {
			missing = append(missing, i)
		}
	}
	// Datastores to spread the VMs over (PRO-9). One = every VM there; several
	// = round-robin by VM index; none = let the driver report it's required.
	datastores := d.Placement.Datastores()
	var (
		wg       sync.WaitGroup
		slots    = make(chan struct{}, createParallelism)
		mu       sync.Mutex
		firstErr error
	)
	for _, i := range missing {
		wg.Add(1)
		slots <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-slots }()
			name := spec.VMName(i)
			placement := d.Placement
			if len(datastores) > 0 {
				placement = placement.WithDatastore(datastores[(i-1)%len(datastores)])
			}
			ref, err := driver.CreateVM(ctx, hypervisor.VMSpec{
				Name:           name,
				VCPUs:          spec.VCPUs,
				MemoryMiB:      spec.MemoryMiB,
				Disks:          diskSpecs(spec.DisksPerVM, spec.DiskSizeGiB, !spec.Thick),
				Tag:            spec.Tag,
				DeploymentID:   d.ID,
				DeploymentName: d.Name,
				Placement:      placement,
			})
			var mac string
			if err == nil {
				// The hypervisor assigns the MAC at creation; the netboot
				// script endpoint matches booting VMs by it.
				if vm, gerr := driver.GetVM(ctx, ref); gerr == nil && vm != nil {
					mac = vm.MAC
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("creating VM %s: %w", name, err)
				}
				return
			}
			if err := o.store.AddManagedVM(&model.ManagedVM{
				DeploymentID: d.ID, Name: name, Ref: ref, MAC: mac,
				DiskCount: spec.DisksPerVM, DiskSizeGiB: spec.DiskSizeGiB,
			}); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("recording VM %s: %w", name, err)
				return
			}
			stats.VMsCreated++
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr // already-created VMs are recorded; a retry continues
	}

	// 2. Grow existing VMs: add disks, then extend smaller ones.
	for _, vm := range actual {
		if add := spec.DisksPerVM - vm.DiskCount; add > 0 {
			if err := driver.AddDisks(ctx, vm.Ref, diskSpecs(add, spec.DiskSizeGiB, !spec.Thick)); err != nil {
				return fmt.Errorf("adding disks to %s: %w", vm.Name, err)
			}
			stats.DisksAdded += add
		}
		if vm.DiskSizeGiB < spec.DiskSizeGiB {
			if err := driver.ExtendDisks(ctx, vm.Ref, spec.DiskSizeGiB); err != nil {
				return fmt.Errorf("extending disks of %s: %w", vm.Name, err)
			}
			stats.DisksExtended++
		}
		if vm.DiskCount != spec.DisksPerVM || vm.DiskSizeGiB != spec.DiskSizeGiB {
			if err := o.store.UpdateManagedVMDisks(vm.ID, spec.DisksPerVM, spec.DiskSizeGiB); err != nil {
				return err
			}
		}
	}
	return nil
}

// teardown deletes every managed VM, then the records (PRO-7: the
// deployment row and run history stay).
// DetectConflicts reports target VM names already on the hypervisor that are
// owned by another/unknown deployment — the deploy-time warning. Opens its own
// driver (the UI calls it before deploying).
func (o *Orchestrator) DetectConflicts(d *model.Deployment) ([]model.VMConflict, error) {
	driver, err := o.OpenDriver(context.Background(), d.ConnectionID)
	if err != nil {
		return nil, err
	}
	defer driver.Close()
	actual, err := o.store.ListManagedVMs(d.ID)
	if err != nil {
		return nil, err
	}
	byName := make(map[string]*model.ManagedVM, len(actual))
	for _, vm := range actual {
		byName[vm.Name] = vm
	}
	return o.findConflicts(context.Background(), d, driver, byName)
}

// findConflicts returns target VM names present on the hypervisor that belong
// to another or an unknown deployment (not this one, not already recorded as
// ours). Owner display is resolved from the store when the owner still exists.
func (o *Orchestrator) findConflicts(ctx context.Context, d *model.Deployment, driver hypervisor.Driver, recorded map[string]*model.ManagedVM) ([]model.VMConflict, error) {
	names := make([]string, 0, d.Spec.VMCount)
	for i := 1; i <= d.Spec.VMCount; i++ {
		if name := d.Spec.VMName(i); recorded[name] == nil {
			names = append(names, name) // ours-and-recorded never conflicts
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	found, err := driver.FindVMs(ctx, d.Placement, names)
	if err != nil {
		return nil, err
	}
	var out []model.VMConflict
	for _, vm := range found {
		if vm.OwnerID == d.ID {
			continue // already ours (e.g. half-created earlier) — adopts cleanly
		}
		c := model.VMConflict{Name: vm.Name, Ref: vm.Ref, OwnerID: vm.OwnerID, OwnerName: vm.OwnerName}
		if vm.OwnerID != "" {
			if owner, err := o.store.GetDeployment(vm.OwnerID); err == nil {
				c.OwnerKnown = true
				c.OwnerName = owner.Name
				since := owner.CreatedAt
				c.OwnerSince = &since
			}
		}
		out = append(out, c)
	}
	return out, nil
}

// conflictSummary renders a short owner-attributed list for an abort error.
func conflictSummary(conflicts []model.VMConflict) string {
	parts := make([]string, 0, len(conflicts))
	for _, c := range conflicts {
		owner := c.OwnerName
		if owner == "" {
			owner = "unknown"
		}
		parts = append(parts, c.Name+"→"+owner)
		if len(parts) == 5 && len(conflicts) > 5 {
			parts = append(parts, "…")
			break
		}
	}
	return strings.Join(parts, ", ")
}

func (o *Orchestrator) teardown(ctx context.Context, d *model.Deployment, driver hypervisor.Driver, stats *jobStats) error {
	vms, err := o.store.ListManagedVMs(d.ID)
	if err != nil {
		return err
	}
	for _, vm := range vms {
		if err := driver.DeleteVM(ctx, vm.Ref); err != nil {
			return fmt.Errorf("deleting VM %s: %w", vm.Name, err)
		}
		if err := o.store.DeleteManagedVM(vm.ID); err != nil {
			return err
		}
		stats.VMsDeleted++
	}
	return nil
}

func diskSpecs(count, sizeGiB int, thin bool) []hypervisor.DiskSpec {
	disks := make([]hypervisor.DiskSpec, count)
	for i := range disks {
		disks[i] = hypervisor.DiskSpec{SizeGiB: sizeGiB, Thin: thin}
	}
	return disks
}
