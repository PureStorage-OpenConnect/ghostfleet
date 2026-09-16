package orch

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/hypervisor"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/hypervisor/fake"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/secrets"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/store"
)

type fixture struct {
	orch   *Orchestrator
	store  *store.Store
	driver *fake.Driver
	dep    *model.Deployment
}

func setup(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secrets.Open(filepath.Join(dir, "key"))
	if err != nil {
		t.Fatal(err)
	}

	driver := fake.New()
	o := New(st, box, hypervisor.Registry{"fake": driver.Factory})

	blob, _ := box.Encrypt("pw")
	conn, err := st.CreateConnection(&model.Connection{
		Name: "fake", Plugin: "fake", Endpoint: "fake://", Username: "u",
	}, blob)
	if err != nil {
		t.Fatal(err)
	}

	spec := model.ProfileSpec{
		VMCount: 5, NamePrefix: "ghost", DisksPerVM: 2,
		DiskSizeGiB: 10, DataPerDiskGiB: 5,
		CompressPercent: 50, DedupePercent: 10, CrossVMDedupePercent: 10,
		ChangePercent: 5, GrowthPercent: 2, Tag: "fleet-1",
	}
	spec.ApplyDefaults()
	p, err := st.CreateProfile("prof", spec)
	if err != nil {
		t.Fatal(err)
	}
	dep, err := st.CreateDeployment(&model.Deployment{
		Name: "dep", ProfileID: p.ID, ProfileVersion: 1, ConnectionID: conn.ID,
		Spec: spec, Placement: model.Placement{"cluster": "C0", "datastore": "LocalDS_0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{orch: o, store: st, driver: driver, dep: dep}
}

// waitIdle waits until the deployment job finishes.
func (f *fixture) waitIdle(t *testing.T) *model.Deployment {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		d, err := f.store.GetDeployment(f.dep.ID)
		if err != nil {
			t.Fatal(err)
		}
		switch d.Status {
		case model.DeploymentReady, model.DeploymentError, model.DeploymentDeleted:
			return d
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish in time")
	return nil
}

func TestDeployCreatesFleet(t *testing.T) {
	f := setup(t)
	run, err := f.orch.StartDeploy(f.dep, model.OnConflictAbort)
	if err != nil {
		t.Fatalf("StartDeploy: %v", err)
	}
	if run.Type != model.RunDeploy {
		t.Fatalf("run type = %q, want deploy", run.Type)
	}

	d := f.waitIdle(t)
	if d.Status != model.DeploymentReady {
		t.Fatalf("status = %q, want ready", d.Status)
	}
	if f.driver.VMCount() != 5 {
		t.Fatalf("driver has %d VMs, want 5", f.driver.VMCount())
	}
	if disks := f.driver.Disks("ghost-0003"); len(disks) != 2 || disks[0].SizeGiB != 10 || !disks[0].Thin {
		t.Fatalf("ghost-0003 disks: %+v", disks)
	}
	if tag := f.driver.Tag("ghost-0001"); tag != "fleet-1" {
		t.Fatalf("tag = %q, want fleet-1", tag)
	}
	vms, _ := f.store.ListManagedVMs(f.dep.ID)
	if len(vms) != 5 {
		t.Fatalf("store records %d VMs, want 5", len(vms))
	}
}

func TestDeployRoundRobinsDatastores(t *testing.T) {
	f := setup(t)
	// Place a deployment over three datastores (newline-separated, as the UI
	// sends them). VMs should be spread evenly by index.
	d, err := f.store.CreateDeployment(&model.Deployment{
		Name: "dep-rr", ProfileID: f.dep.ProfileID, ProfileVersion: 1,
		ConnectionID: f.dep.ConnectionID, Spec: f.dep.Spec,
		Placement: model.Placement{"cluster": "C0", "datastore": "ds-a\nds-b\nds-c"},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.dep = d // so waitIdle tracks this deployment

	if _, err := f.orch.StartDeploy(d, model.OnConflictAbort); err != nil {
		t.Fatalf("StartDeploy: %v", err)
	}
	f.waitIdle(t)

	// 5 VMs over 3 datastores, round-robin by 1-based index:
	// 1→ds-a 2→ds-b 3→ds-c 4→ds-a 5→ds-b.
	want := map[string]string{
		"ghost-0001": "ds-a", "ghost-0002": "ds-b", "ghost-0003": "ds-c",
		"ghost-0004": "ds-a", "ghost-0005": "ds-b",
	}
	counts := map[string]int{}
	for name, wantDS := range want {
		if got := f.driver.Datastore(name); got != wantDS {
			t.Errorf("%s on datastore %q, want %q", name, got, wantDS)
		}
		counts[f.driver.Datastore(name)]++
	}
	if counts["ds-a"] != 2 || counts["ds-b"] != 2 || counts["ds-c"] != 1 {
		t.Errorf("uneven spread: %v", counts)
	}
}

func TestIncrementalBeforeFillRejected(t *testing.T) {
	f := setup(t)
	f.orch.StartDeploy(f.dep, model.OnConflictAbort)
	f.waitIdle(t)

	_, err := f.orch.StartFill(f.dep, model.RunIncremental)
	var pre ErrPrecondition
	if !errors.As(err, &pre) {
		t.Fatalf("incremental before fill: got %v, want ErrPrecondition", err)
	}
	// A rejected start must release the lock so a real run can follow.
	if f.orch.IsActive(f.dep.ID) {
		t.Fatal("deployment still marked active after a rejected fill")
	}
}

func TestCancelFill(t *testing.T) {
	f := setup(t)
	f.orch.StartDeploy(f.dep, model.OnConflictAbort)
	f.waitIdle(t)

	// The fake VMs never report progress, so the fill loops until cancelled.
	run, err := f.orch.StartFill(f.dep, model.RunInitialFill)
	if err != nil {
		t.Fatalf("StartFill: %v", err)
	}
	if err := f.orch.Cancel(f.dep.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, _ := f.store.GetRun(run.ID)
		if r.Status == model.RunCancelled {
			// Unfinished VMs are reset and the lock is released.
			if f.orch.IsActive(f.dep.ID) {
				t.Fatal("still active after cancel")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("fill run was not cancelled in time")
}

// injectForeignVM puts a VM occupying a target name, owned by another deployment.
func (f *fixture) injectForeignVM(t *testing.T, name, ownerID, ownerName string) string {
	t.Helper()
	ref, err := f.driver.CreateVM(context.Background(), hypervisor.VMSpec{
		Name: name, DeploymentID: ownerID, DeploymentName: ownerName, Placement: f.dep.Placement,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestCrossDeploymentConflictAborts(t *testing.T) {
	f := setup(t)
	f.injectForeignVM(t, f.dep.Spec.VMName(1), "other-dep", "Other")

	conflicts, err := f.orch.DetectConflicts(f.dep)
	if err != nil {
		t.Fatalf("DetectConflicts: %v", err)
	}
	if len(conflicts) != 1 || conflicts[0].Name != f.dep.Spec.VMName(1) || conflicts[0].OwnerID != "other-dep" {
		t.Fatalf("conflicts: %+v", conflicts)
	}

	// Default abort: the deploy fails and creates nothing beyond the foreign VM.
	f.orch.StartDeploy(f.dep, model.OnConflictAbort)
	d := f.waitIdle(t)
	if d.Status != model.DeploymentError {
		t.Fatalf("abort: status = %q, want error", d.Status)
	}
	if f.driver.VMCount() != 1 {
		t.Fatalf("abort created VMs: count = %d, want 1 (just the foreign VM)", f.driver.VMCount())
	}
}

func TestCrossDeploymentAdoptAndClean(t *testing.T) {
	// Adopt: the foreign VM is taken over (same ref), fleet completes.
	f := setup(t)
	foreignRef := f.injectForeignVM(t, f.dep.Spec.VMName(1), "other-dep", "Other")
	f.orch.StartDeploy(f.dep, model.OnConflictAdopt)
	if d := f.waitIdle(t); d.Status != model.DeploymentReady {
		t.Fatalf("adopt: status = %q, want ready", d.Status)
	}
	if f.driver.VMCount() != 5 {
		t.Fatalf("adopt: VM count = %d, want 5", f.driver.VMCount())
	}
	vms, _ := f.store.ListManagedVMs(f.dep.ID)
	var adoptedRef string
	for _, vm := range vms {
		if vm.Name == f.dep.Spec.VMName(1) {
			adoptedRef = vm.Ref
		}
	}
	if adoptedRef != foreignRef {
		t.Fatalf("adopted ref = %q, want the foreign VM's %q", adoptedRef, foreignRef)
	}

	// Clean: a foreign VM is deleted and replaced with a fresh one.
	g := setup(t)
	staleRef := g.injectForeignVM(t, g.dep.Spec.VMName(2), "other-dep", "Other")
	g.orch.StartDeploy(g.dep, model.OnConflictClean)
	if d := g.waitIdle(t); d.Status != model.DeploymentReady {
		t.Fatalf("clean: status = %q, want ready", d.Status)
	}
	if vm, _ := g.driver.GetVM(context.Background(), staleRef); vm != nil {
		t.Fatalf("clean: foreign VM %q should have been deleted", staleRef)
	}
	if g.driver.VMCount() != 5 {
		t.Fatalf("clean: VM count = %d, want 5", g.driver.VMCount())
	}
}

func TestResumeInterruptedReconcile(t *testing.T) {
	f := setup(t)
	// Simulate a deploy interrupted by a controller restart: a run left
	// 'running' and the deployment 'deploying', but no VMs created yet.
	run, err := f.store.CreateRun(f.dep.ID, model.RunDeploy)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.StartRun(run.ID); err != nil {
		t.Fatal(err)
	}
	f.store.SetDeploymentStatus(f.dep.ID, model.DeploymentDeploying)

	f.orch.ResumeInterrupted()

	d := f.waitIdle(t)
	if d.Status != model.DeploymentReady {
		t.Fatalf("status = %q, want ready", d.Status)
	}
	if f.driver.VMCount() != 5 {
		t.Fatalf("driver has %d VMs, want 5", f.driver.VMCount())
	}
	r, _ := f.store.GetRun(run.ID)
	if r.Status != model.RunSucceeded {
		t.Fatalf("resumed run status = %q, want succeeded", r.Status)
	}
}

func TestFillStallFailsRun(t *testing.T) {
	// Shrink the poll + stall window so the test doesn't wait minutes.
	origPoll, origStall := fillPollInterval, fillStallLimit
	fillPollInterval, fillStallLimit = 10*time.Millisecond, 40*time.Millisecond
	defer func() { fillPollInterval, fillStallLimit = origPoll, origStall }()

	f := setup(t)
	f.orch.StartDeploy(f.dep, model.OnConflictAbort)
	f.waitIdle(t)

	// Fake VMs never report progress, so the fill never advances → stalls.
	run, err := f.orch.StartFill(f.dep, model.RunInitialFill)
	if err != nil {
		t.Fatalf("StartFill: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, _ := f.store.GetRun(run.ID)
		if r.Status == model.RunFailed {
			var stats struct {
				Error string `json:"error"`
			}
			json.Unmarshal([]byte(r.Stats), &stats)
			if !strings.Contains(stats.Error, "stalled") {
				t.Fatalf("failed for the wrong reason: %q", stats.Error)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("stalled fill was not failed in time")
}

// TestFillPowerCyclesWedgedVMsOnEntry: a fill that starts against a fleet
// that is already powered on must hard-reset only the VMs without a live
// agent (they are unreachable in any other way — e.g. wedged in PXE), and
// leave VMs with a heartbeating agent untouched (they pick the work order up
// on their next heartbeat).
func TestFillPowerCyclesWedgedVMsOnEntry(t *testing.T) {
	origPoll := fillPollInterval
	fillPollInterval = 10 * time.Millisecond
	defer func() { fillPollInterval = origPoll }()

	f := setup(t)
	f.orch.StartDeploy(f.dep, model.OnConflictAbort)
	f.waitIdle(t)
	vms, _ := f.store.ListManagedVMs(f.dep.ID)

	// The whole fleet was left powered on (as after a failed prior run); all
	// agents except ghost-0005's are alive.
	for _, vm := range vms {
		f.driver.PowerOn(context.Background(), vm.Ref)
		if vm.Name != "ghost-0005" {
			if err := f.store.TouchAgent(vm.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	baseline := len(f.driver.PowerOps())

	if _, err := f.orch.StartFill(f.dep, model.RunInitialFill); err != nil {
		t.Fatalf("StartFill: %v", err)
	}
	// Wait for the entry boot pass, then stop the run.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.driver.PowerOps()) > baseline {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.orch.Cancel(f.dep.ID)

	ops := f.driver.PowerOps()[baseline:]
	var cycled []string
	for _, op := range ops {
		if strings.HasPrefix(op, "off:") || strings.HasPrefix(op, "on:") {
			cycled = append(cycled, op)
		}
	}
	want := []string{"off:ghost-0005", "on:ghost-0005"}
	if len(cycled) != 2 || cycled[0] != want[0] || cycled[1] != want[1] {
		t.Fatalf("power ops after fill start = %v, want exactly %v (alive VMs must be untouched)", cycled, want)
	}
}

// TestFillPowersOnFreshAgentPoweredOffVMs: an incremental started right after
// the initial fill's post-run shutdown finds every agent heartbeat still
// fresh (<30 s) but every VM powered off. The boot pass must go by the power
// state and power the fleet on, not trust the heartbeats and boot nothing.
func TestFillPowersOnFreshAgentPoweredOffVMs(t *testing.T) {
	origPoll := fillPollInterval
	fillPollInterval = 10 * time.Millisecond
	defer func() { fillPollInterval = origPoll }()

	f := setup(t)
	f.orch.StartDeploy(f.dep, model.OnConflictAbort)
	f.waitIdle(t)
	vms, _ := f.store.ListManagedVMs(f.dep.ID)

	for _, vm := range vms {
		f.driver.PowerOff(context.Background(), vm.Ref)
		if err := f.store.TouchAgent(vm.ID); err != nil {
			t.Fatal(err)
		}
	}
	baseline := len(f.driver.PowerOps())

	if _, err := f.orch.StartFill(f.dep, model.RunInitialFill); err != nil {
		t.Fatalf("StartFill: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(f.driver.PowerOps())-baseline >= len(vms) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	f.orch.Cancel(f.dep.ID)

	on := map[string]bool{}
	for _, op := range f.driver.PowerOps()[baseline:] {
		if strings.HasPrefix(op, "off:") {
			t.Fatalf("powered-off VM was power-cycled (%s); a plain power-on is enough", op)
		}
		if strings.HasPrefix(op, "on:") {
			on[strings.TrimPrefix(op, "on:")] = true
		}
	}
	for _, vm := range vms {
		if !on[vm.Name] {
			t.Errorf("%s: powered off with a fresh heartbeat, but never powered on", vm.Name)
		}
	}
}

// TestFillAfterShutdownResetsAgent: the post-run shutdown clears agent
// liveness, so a powered-off VM never shows an online agent.
func TestFillAfterShutdownResetsAgent(t *testing.T) {
	f := setup(t)
	f.orch.StartDeploy(f.dep, model.OnConflictAbort)
	f.waitIdle(t)
	vms, _ := f.store.ListManagedVMs(f.dep.ID)
	for _, vm := range vms {
		if err := f.store.TouchAgent(vm.ID); err != nil {
			t.Fatal(err)
		}
	}
	driver, err := f.orch.OpenDriver(context.Background(), f.dep.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	defer driver.Close()
	f.dep.Spec.AfterFill = model.AfterFillShutdown
	f.orch.applyAfterFill(context.Background(), driver, f.dep, vms)

	after, _ := f.store.ListManagedVMs(f.dep.ID)
	for _, vm := range after {
		if vm.AgentStatus != model.AgentNone || vm.AgentSeenAt != nil {
			t.Errorf("%s: agent status %q seen %v after shutdown, want none/nil", vm.Name, vm.AgentStatus, vm.AgentSeenAt)
		}
		if f.driver.State(vm.Name) != hypervisor.StatePoweredOff {
			t.Errorf("%s: not powered off", vm.Name)
		}
	}
}

// TestFillBootWatchdogFailsUnbootableVMAlone: a VM whose agent never
// registers is power-cycled up to maxBootAttempts, then marked failed by
// itself; the run finishes as a partial failure that names the culprit and
// keeps the healthy VMs' progress, and the post-fill shutdown still applies
// so the fleet ends powered off (no wedged-powered-on carry-over).
func TestFillBootWatchdogFailsUnbootableVMAlone(t *testing.T) {
	origPoll, origBoot, origTries, origStall := fillPollInterval, bootTimeout, maxBootAttempts, fillStallLimit
	fillPollInterval, bootTimeout, maxBootAttempts, fillStallLimit = 5*time.Millisecond, 30*time.Millisecond, 2, 10*time.Second
	defer func() {
		fillPollInterval, bootTimeout, maxBootAttempts, fillStallLimit = origPoll, origBoot, origTries, origStall
	}()

	f := setup(t)
	f.orch.StartDeploy(f.dep, model.OnConflictAbort)
	f.waitIdle(t)
	vms, _ := f.store.ListManagedVMs(f.dep.ID)

	run, err := f.orch.StartFill(f.dep, model.RunInitialFill)
	if err != nil {
		t.Fatalf("StartFill: %v", err)
	}
	// Four agents report done; ghost-0005's never registers.
	for _, vm := range vms {
		if vm.Name != "ghost-0005" {
			if err := f.store.ReportFillProgress(vm.ID, 1<<30, 100, true, ""); err != nil {
				t.Fatal(err)
			}
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		r, _ := f.store.GetRun(run.ID)
		if r.Status == model.RunFailed {
			var stats struct {
				Error        string `json:"error"`
				VMsDone      int    `json:"vmsDone"`
				VMsFailed    int    `json:"vmsFailed"`
				BytesWritten int64  `json:"bytesWritten"`
			}
			json.Unmarshal([]byte(r.Stats), &stats)
			if !strings.Contains(stats.Error, "ghost-0005") || !strings.Contains(stats.Error, "never registered") {
				t.Fatalf("error does not name the culprit: %q", stats.Error)
			}
			if stats.VMsDone != 4 || stats.VMsFailed != 1 || stats.BytesWritten != 4<<30 {
				t.Fatalf("stats lost the partial result: %+v", stats)
			}
			// The watchdog must have power-cycled ghost-0005 before giving up.
			var resets int
			for _, op := range f.driver.PowerOps() {
				if op == "off:ghost-0005" {
					resets++
				}
			}
			if resets == 0 {
				t.Fatal("ghost-0005 was never power-cycled by the watchdog")
			}
			// afterFill=shutdown applies on failure too: fleet ends powered off.
			for _, vm := range vms {
				if s := f.driver.State(vm.Name); s != hypervisor.StatePoweredOff {
					t.Fatalf("%s state = %q after failed run, want poweredOff", vm.Name, s)
				}
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not fail in time")
}

func TestScaleUpAddsVMsAndDisks(t *testing.T) {
	f := setup(t)
	f.orch.StartDeploy(f.dep, model.OnConflictAbort)
	f.waitIdle(t)

	// Grow: more VMs, more disks per VM, bigger disks.
	grown := f.dep.Spec
	grown.VMCount = 7
	grown.DisksPerVM = 3
	grown.DiskSizeGiB = 20
	if err := f.store.UpdateDeploymentSpec(f.dep.ID, grown); err != nil {
		t.Fatal(err)
	}
	d, _ := f.store.GetDeployment(f.dep.ID)

	run, err := f.orch.StartDeploy(d, model.OnConflictAbort)
	if err != nil {
		t.Fatalf("scale-up StartDeploy: %v", err)
	}
	if run.Type != model.RunScaleUp {
		t.Fatalf("run type = %q, want scale-up", run.Type)
	}
	d = f.waitIdle(t)
	if d.Status != model.DeploymentReady {
		t.Fatalf("status = %q", d.Status)
	}
	if f.driver.VMCount() != 7 {
		t.Fatalf("driver has %d VMs, want 7", f.driver.VMCount())
	}
	// An original VM gained a disk and its old disks were extended.
	disks := f.driver.Disks("ghost-0001")
	if len(disks) != 3 {
		t.Fatalf("ghost-0001 has %d disks, want 3", len(disks))
	}
	for i, disk := range disks {
		if disk.SizeGiB != 20 {
			t.Fatalf("disk %d size = %d, want 20", i, disk.SizeGiB)
		}
	}
}

func TestPartialFailureThenRetry(t *testing.T) {
	f := setup(t)
	f.driver.FailCreates = 2 // two of five creations fail

	f.orch.StartDeploy(f.dep, model.OnConflictAbort)
	d := f.waitIdle(t)
	if d.Status != model.DeploymentError {
		t.Fatalf("status = %q, want error", d.Status)
	}
	created := f.driver.VMCount()
	if created == 0 || created == 5 {
		t.Fatalf("expected partial creation, got %d", created)
	}

	// Retry completes the missing VMs without duplicating existing ones.
	if _, err := f.orch.StartDeploy(d, model.OnConflictAbort); err != nil {
		t.Fatalf("retry: %v", err)
	}
	d = f.waitIdle(t)
	if d.Status != model.DeploymentReady {
		t.Fatalf("status after retry = %q", d.Status)
	}
	if f.driver.VMCount() != 5 {
		t.Fatalf("driver has %d VMs after retry, want 5", f.driver.VMCount())
	}
	runs, _ := f.store.ListRuns(f.dep.ID)
	if len(runs) != 2 {
		t.Fatalf("want 2 runs, got %d", len(runs))
	}
}

func TestTeardownKeepsHistory(t *testing.T) {
	f := setup(t)
	f.orch.StartDeploy(f.dep, model.OnConflictAbort)
	f.waitIdle(t)

	if _, err := f.orch.StartTeardown(f.dep); err != nil {
		t.Fatalf("StartTeardown: %v", err)
	}
	d := f.waitIdle(t)
	if d.Status != model.DeploymentDeleted {
		t.Fatalf("status = %q, want deleted", d.Status)
	}
	if f.driver.VMCount() != 0 {
		t.Fatalf("driver still has %d VMs", f.driver.VMCount())
	}
	vms, _ := f.store.ListManagedVMs(f.dep.ID)
	if len(vms) != 0 {
		t.Fatalf("store still records %d VMs", len(vms))
	}
	runs, _ := f.store.ListRuns(f.dep.ID)
	if len(runs) != 2 { // deploy + teardown survive (PRO-7)
		t.Fatalf("want 2 runs in history, got %d", len(runs))
	}
}

func TestConcurrentJobRejected(t *testing.T) {
	f := setup(t)
	if _, err := f.orch.StartDeploy(f.dep, model.OnConflictAbort); err != nil {
		t.Fatal(err)
	}
	_, err := f.orch.StartDeploy(f.dep, model.OnConflictAbort)
	if _, ok := err.(ErrBusy); !ok {
		t.Fatalf("expected ErrBusy, got %v", err)
	}
	f.waitIdle(t)
}
