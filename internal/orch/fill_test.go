package orch

import (
	"testing"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

// startFill retries on ErrBusy: a finished job sets the deployment status
// just before its deferred lock release, so there's a brief "ready but still
// locked" window after waitIdle returns (benign in production — a client
// gets a transient 409 and retries).
func startFill(t *testing.T, f *fixture, dep *model.Deployment, runType string) *model.Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		run, err := f.orch.StartFill(dep, runType)
		if err == nil {
			return run
		}
		if _, busy := err.(ErrBusy); !busy || time.Now().After(deadline) {
			t.Fatalf("StartFill: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestFillFlow drives a fill end-to-end with the fake driver: start the run,
// confirm each VM gets a work order, simulate agents reporting done, and
// check the run succeeds with aggregate stats.
func TestFillFlow(t *testing.T) {
	f := setup(t)
	f.orch.StartDeploy(f.dep, model.OnConflictAbort)
	f.waitIdle(t)

	run := startFill(t, f, f.dep, model.RunInitialFill)
	if run.Type != model.RunInitialFill {
		t.Fatalf("run type = %q", run.Type)
	}

	vms, _ := f.store.ListManagedVMs(f.dep.ID)
	if len(vms) != 5 {
		t.Fatalf("want 5 vms, got %d", len(vms))
	}

	// Each VM: fetch its work order (promotes pending→working), then report done.
	for _, vm := range vms {
		cur, _ := f.store.GetManagedVMByToken(vm.BootToken)
		action, wo := f.orch.WorkOrderForToken(cur)
		if action != "fill" || wo == nil {
			t.Fatalf("vm %s: action=%q wo=%v", vm.Name, action, wo)
		}
		if wo.RunID != run.ID || len(wo.Disks) != f.dep.Spec.DisksPerVM {
			t.Fatalf("vm %s work order mismatch: %+v", vm.Name, wo)
		}
		// Simulate the agent finishing.
		if err := f.store.ReportFillProgress(vm.ID, wo.Disks[0].GlobalBytes+wo.Disks[0].LocalBytes, 100, true, ""); err != nil {
			t.Fatalf("report: %v", err)
		}
	}

	// The fill job polls every 5s; wait for it to observe completion.
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		r, _ := f.store.GetRun(run.ID)
		if r.Status == model.RunSucceeded {
			return
		}
		if r.Status == model.RunFailed {
			t.Fatalf("fill failed: %s", r.Stats)
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("fill run did not succeed in time")
}

func TestWorkOrderConcurrencyGate(t *testing.T) {
	f := setup(t)
	// Cap parallelism at 2.
	spec := f.dep.Spec
	spec.MaxParallelVMs = 2
	f.store.UpdateDeploymentSpec(f.dep.ID, spec)
	d, _ := f.store.GetDeployment(f.dep.ID)

	f.orch.StartDeploy(d, model.OnConflictAbort)
	f.waitIdle(t)
	startFill(t, f, d, model.RunInitialFill)

	vms, _ := f.store.ListManagedVMs(d.ID)
	working := 0
	for _, vm := range vms {
		cur, _ := f.store.GetManagedVMByToken(vm.BootToken)
		if action, _ := f.orch.WorkOrderForToken(cur); action == "fill" {
			working++
		}
	}
	if working != 2 {
		t.Fatalf("with MaxParallelVMs=2, %d VMs got work orders, want 2", working)
	}
}
