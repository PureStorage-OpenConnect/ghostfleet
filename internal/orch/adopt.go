package orch

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/datagen"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

// Adoption turns a discovered VM (a machine that PXE-booted with an unknown
// MAC and whose disks carry GhostFleet identity markers — typically a backup
// restore) back into a managed VM. Two modes:
//
//   - rebind: the restored VM takes the original's place in its original
//     deployment — for delete-then-restore-in-place. The existing ManagedVM
//     record is re-pointed at the new MAC/ref; nothing else changes.
//   - new deployment: the restored VM joins a fresh (or an existing) adopted
//     deployment, leaving the original deployment untouched — for
//     restore-alongside, where the copy exists to be verified and dumped.
//
// Both end by marking the discovery record adopted; the discovery agent is
// told to reboot on its next heartbeat and comes back up as a managed VM
// (its MAC now resolves to a ManagedVM record).

// AdoptResult reports what an adoption did.
type AdoptResult struct {
	Deployment *model.Deployment `json:"deployment"`
	VM         *model.ManagedVM  `json:"vm"`
	// Warning flags a succeeded adoption that leaves something for the
	// operator to check (e.g. a rebind while the original VM still exists).
	Warning string `json:"warning,omitempty"`
}

// discoveredIdentity parses a discovered VM's inspection report and extracts
// its GhostFleet identity. Errors if the VM was never inspected or none of
// its disks carry an identity marker.
func discoveredIdentity(dv *model.DiscoveredVM) (*datagen.DiscoveryReport, *datagen.IdentityMarker, error) {
	if dv.Inspection == "" {
		return nil, nil, ErrPrecondition{"the discovered VM has not reported a disk inspection yet"}
	}
	var report datagen.DiscoveryReport
	if err := json.Unmarshal([]byte(dv.Inspection), &report); err != nil {
		return nil, nil, fmt.Errorf("parsing inspection report: %w", err)
	}
	id := report.GhostIdentity()
	if id == nil {
		return nil, nil, ErrPrecondition{"no GhostFleet identity marker found on the discovered VM's disks — not adoptable"}
	}
	return &report, id, nil
}

// AdoptRebind re-points the original ManagedVM record (found via the disks'
// identity marker) at the discovered VM: restore-in-place adoption.
func (o *Orchestrator) AdoptRebind(ctx context.Context, dv *model.DiscoveredVM) (*AdoptResult, error) {
	if dv.Status == model.DiscoveredAdopted {
		return nil, ErrPrecondition{"this VM is already adopted"}
	}
	_, id, err := discoveredIdentity(dv)
	if err != nil {
		return nil, err
	}
	d, err := o.store.GetDeployment(id.DeploymentID)
	if err != nil || d.Status == model.DeploymentDeleted {
		return nil, ErrPrecondition{fmt.Sprintf(
			"original deployment %q no longer exists — adopt into a new deployment instead", id.DeploymentName)}
	}
	vm, err := o.store.GetManagedVMByName(d.ID, id.VMName)
	if err != nil {
		return nil, ErrPrecondition{fmt.Sprintf(
			"deployment %q has no VM record %q anymore — adopt into a new deployment instead", d.Name, id.VMName)}
	}

	driver, err := o.OpenDriver(ctx, d.ConnectionID)
	if err != nil {
		return nil, fmt.Errorf("opening hypervisor connection: %w", err)
	}
	defer driver.Close()
	hv, err := driver.FindVMByMAC(ctx, dv.MAC)
	if err != nil {
		return nil, fmt.Errorf("locating VM by MAC %s: %w", dv.MAC, err)
	}
	if hv == nil {
		return nil, ErrPrecondition{fmt.Sprintf(
			"no VM with MAC %s found on the deployment's hypervisor connection", dv.MAC)}
	}

	res := &AdoptResult{Deployment: d}
	// Rebind is rewriting by design: the restored copy takes the record over.
	// If the VM the record used to point at still exists, it just lost its
	// management — surface that instead of hiding it.
	if hv.Ref != vm.Ref {
		if old, err := driver.GetVM(ctx, vm.Ref); err == nil && old != nil {
			res.Warning = fmt.Sprintf(
				"the previous VM %q (%s) still exists on the hypervisor and is now unmanaged — delete it there if it should be gone",
				old.Name, vm.Ref)
		}
	}
	if err := o.store.RebindManagedVM(vm.ID, dv.MAC, hv.Ref); err != nil {
		return nil, err
	}
	if err := o.store.SetDiscoveredStatus(dv.ID, model.DiscoveredAdopted); err != nil {
		return nil, err
	}
	vm, err = o.store.GetManagedVM(vm.ID)
	if err != nil {
		return nil, err
	}
	res.VM = vm
	slog.Info("adopted discovered VM (rebind)", "deployment", d.Name, "vm", vm.Name, "mac", dv.MAC, "ref", hv.Ref)
	return res, nil
}

// AdoptNew adopts the discovered VM into a new deployment (or, with
// joinDeploymentID, an existing adopted one), reconstructed from the disks'
// identity marker. The original deployment is not touched. name and
// connectionID are optional: name defaults to "adopted-<vm name>", the
// connection to the original deployment's (when it still exists).
func (o *Orchestrator) AdoptNew(ctx context.Context, dv *model.DiscoveredVM, name, connectionID, joinDeploymentID string) (*AdoptResult, error) {
	if dv.Status == model.DiscoveredAdopted {
		return nil, ErrPrecondition{"this VM is already adopted"}
	}
	_, id, err := discoveredIdentity(dv)
	if err != nil {
		return nil, err
	}

	// Resolve the target deployment first (join) or its connection (create).
	var join *model.Deployment
	if joinDeploymentID != "" {
		join, err = o.store.GetDeployment(joinDeploymentID)
		if err != nil {
			return nil, err
		}
		if join.Origin != model.OriginAdopted || join.Status == model.DeploymentDeleted {
			return nil, ErrPrecondition{"target deployment must be an existing adopted deployment"}
		}
		connectionID = join.ConnectionID
	}
	if connectionID == "" {
		// Default to the original deployment's connection — the restored VM
		// almost always lives on the same hypervisor as its source.
		orig, err := o.store.GetDeployment(id.DeploymentID)
		if err != nil {
			return nil, ErrPrecondition{fmt.Sprintf(
				"original deployment %q is gone; pass a connection to search for the VM", id.DeploymentName)}
		}
		connectionID = orig.ConnectionID
	}

	driver, err := o.OpenDriver(ctx, connectionID)
	if err != nil {
		return nil, fmt.Errorf("opening hypervisor connection: %w", err)
	}
	defer driver.Close()
	hv, err := driver.FindVMByMAC(ctx, dv.MAC)
	if err != nil {
		return nil, fmt.Errorf("locating VM by MAC %s: %w", dv.MAC, err)
	}
	if hv == nil {
		return nil, ErrPrecondition{fmt.Sprintf("no VM with MAC %s found on that hypervisor connection", dv.MAC)}
	}

	d := join
	if d == nil {
		if name == "" {
			name = "adopted-" + hv.Name
		}
		// The effective config comes from the identity marker, so
		// incrementals on the adopted copy behave like the original profile.
		spec := id.Spec
		spec.VMCount = 1
		d = &model.Deployment{
			Name:         name,
			ConnectionID: connectionID,
			Spec:         spec,
			Placement:    model.Placement{},
			Status:       model.DeploymentReady, // its VM already exists
			Origin:       model.OriginAdopted,
		}
		if d, err = o.store.CreateDeployment(d); err != nil {
			return nil, err
		}
	} else {
		// Joining: keep the spec's VM count in step for rate-cap splitting.
		spec := d.Spec
		spec.VMCount++
		if err := o.store.UpdateDeploymentSpec(d.ID, spec); err != nil {
			return nil, err
		}
		d.Spec = spec
	}

	vm := &model.ManagedVM{
		DeploymentID: d.ID,
		Name:         hv.Name, // the restored copy's actual hypervisor name
		Ref:          hv.Ref,
		MAC:          dv.MAC,
		DiskCount:    hv.DiskCount,
		DiskSizeGiB:  id.Spec.DiskSizeGiB,
	}
	if err := o.store.AddManagedVM(vm); err != nil {
		return nil, err
	}
	if err := o.store.SetDiscoveredStatus(dv.ID, model.DiscoveredAdopted); err != nil {
		return nil, err
	}
	slog.Info("adopted discovered VM (new deployment)",
		"deployment", d.Name, "vm", vm.Name, "mac", dv.MAC, "ref", hv.Ref)
	return &AdoptResult{Deployment: d, VM: vm}, nil
}
