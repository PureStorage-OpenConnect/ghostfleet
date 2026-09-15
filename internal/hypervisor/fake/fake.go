// Package fake is an in-memory hypervisor driver for tests and demos.
package fake

import (
	"context"
	"fmt"
	"sync"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/hypervisor"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

// Driver records all operations in memory. Safe for concurrent use.
type Driver struct {
	mu     sync.Mutex
	nextID int
	vms    map[string]*vmRecord

	// FailCreates makes the next N CreateVM calls fail (for retry tests).
	FailCreates int

	powerOps []string // "on:<name>" / "off:<name>" in call order
}

type vmRecord struct {
	vm        hypervisor.VM
	disks     []hypervisor.DiskSpec
	tag       string
	placement model.Placement
	ownerID   string
	ownerName string
}

// New returns an empty fake driver.
func New() *Driver {
	return &Driver{vms: make(map[string]*vmRecord)}
}

// Factory adapts a shared driver instance to the hypervisor.Factory
// signature, so tests can inspect the same instance the orchestrator uses.
func (d *Driver) Factory(context.Context, *model.Connection, string) (hypervisor.Driver, error) {
	return d, nil
}

func (d *Driver) Info(context.Context) (hypervisor.Info, error) {
	return hypervisor.Info{Product: "FakeVisor", Version: "1.0"}, nil
}

func (d *Driver) ListPlacement(context.Context) (hypervisor.PlacementOptions, error) {
	return hypervisor.PlacementOptions{
		Datacenters:   []string{"DC0"},
		Clusters:      []string{"C0"},
		Hosts:         []hypervisor.HostOption{{Name: "esx-01.lab", Cluster: "C0"}},
		ResourcePools: []string{"/DC0/host/C0/Resources/ghostfleet"},
		Datastores: []hypervisor.DatastoreOption{
			{Name: "LocalDS_0", FreeBytes: 500 << 30, CapacityBytes: 1 << 40},
		},
		Networks: []string{"VM Network", "isolated"},
	}, nil
}

func (d *Driver) CreateVM(_ context.Context, spec hypervisor.VMSpec) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.FailCreates > 0 {
		d.FailCreates--
		return "", fmt.Errorf("fake: injected create failure")
	}
	for ref, r := range d.vms {
		if r.vm.Name == spec.Name {
			// Mirror the real driver: CreateVM adopts an existing same-named VM
			// (idempotent retry / cross-deployment adopt) and re-stamps owner.
			r.ownerID, r.ownerName = spec.DeploymentID, spec.DeploymentName
			return ref, nil
		}
	}
	d.nextID++
	ref := fmt.Sprintf("fake-vm-%d", d.nextID)
	d.vms[ref] = &vmRecord{
		vm: hypervisor.VM{
			Ref: ref, Name: spec.Name,
			State: hypervisor.StatePoweredOff, DiskCount: len(spec.Disks),
			MAC: fmt.Sprintf("00:50:56:fa:ke:%02x", d.nextID%256),
		},
		disks:     append([]hypervisor.DiskSpec(nil), spec.Disks...),
		tag:       spec.Tag,
		placement: spec.Placement,
		ownerID:   spec.DeploymentID,
		ownerName: spec.DeploymentName,
	}
	return ref, nil
}

func (d *Driver) AddDisks(_ context.Context, ref string, disks []hypervisor.DiskSpec) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.vms[ref]
	if !ok {
		return fmt.Errorf("fake: no vm %q", ref)
	}
	r.disks = append(r.disks, disks...)
	r.vm.DiskCount = len(r.disks)
	return nil
}

func (d *Driver) ExtendDisks(_ context.Context, ref string, sizeGiB int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.vms[ref]
	if !ok {
		return fmt.Errorf("fake: no vm %q", ref)
	}
	for i := range r.disks {
		if r.disks[i].SizeGiB < sizeGiB {
			r.disks[i].SizeGiB = sizeGiB
		}
	}
	return nil
}

func (d *Driver) GetVM(_ context.Context, ref string) (*hypervisor.VM, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.vms[ref]
	if !ok {
		return nil, nil
	}
	vm := r.vm
	vm.OwnerID, vm.OwnerName = r.ownerID, r.ownerName
	return &vm, nil
}

func (d *Driver) FindVMs(_ context.Context, _ model.Placement, names []string) ([]hypervisor.VM, error) {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []hypervisor.VM
	for _, r := range d.vms {
		if want[r.vm.Name] {
			out = append(out, hypervisor.VM{
				Ref: r.vm.Ref, Name: r.vm.Name, OwnerID: r.ownerID, OwnerName: r.ownerName,
			})
		}
	}
	return out, nil
}

func (d *Driver) FindVMByMAC(_ context.Context, mac string) (*hypervisor.VM, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.vms {
		if r.vm.MAC == mac {
			vm := r.vm
			vm.OwnerID, vm.OwnerName = r.ownerID, r.ownerName
			return &vm, nil
		}
	}
	return nil, nil
}

func (d *Driver) ReadConsole(_ context.Context, ref string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, ok := d.vms[ref]; !ok {
		return "", nil
	}
	return "[fake] console output is only available on real hypervisors\n", nil
}

func (d *Driver) PowerOn(_ context.Context, ref string) error {
	return d.setState(ref, "on", hypervisor.StatePoweredOn)
}

func (d *Driver) PowerOff(_ context.Context, ref string) error {
	return d.setState(ref, "off", hypervisor.StatePoweredOff)
}

func (d *Driver) setState(ref, op string, s hypervisor.VMState) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	r, ok := d.vms[ref]
	if !ok {
		return fmt.Errorf("fake: no vm %q", ref)
	}
	r.vm.State = s
	d.powerOps = append(d.powerOps, op+":"+r.vm.Name)
	return nil
}

func (d *Driver) DeleteVM(_ context.Context, ref string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.vms, ref) // deleting a missing VM is fine by contract
	return nil
}

func (d *Driver) Close() {}

// --- test helpers ---

// AddRawVM plants a VM that GhostFleet did not create — e.g. a backup restore
// that came up with a fresh MAC — and returns its ref.
func (d *Driver) AddRawVM(name, mac string, diskCount int) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextID++
	ref := fmt.Sprintf("fake-vm-%d", d.nextID)
	disks := make([]hypervisor.DiskSpec, diskCount)
	d.vms[ref] = &vmRecord{
		vm: hypervisor.VM{
			Ref: ref, Name: name, State: hypervisor.StatePoweredOff,
			DiskCount: diskCount, MAC: mac,
		},
		disks: disks,
	}
	return ref
}

// VMCount returns the number of existing VMs.
func (d *Driver) VMCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.vms)
}

// Disks returns the disk specs of a VM by name, or nil.
func (d *Driver) Disks(name string) []hypervisor.DiskSpec {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.vms {
		if r.vm.Name == name {
			return append([]hypervisor.DiskSpec(nil), r.disks...)
		}
	}
	return nil
}

// Tag returns the tag attached to a VM by name.
func (d *Driver) Tag(name string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.vms {
		if r.vm.Name == name {
			return r.tag
		}
	}
	return ""
}

// PowerOps returns the power operations performed so far, oldest first, as
// "on:<vm name>" / "off:<vm name>".
func (d *Driver) PowerOps() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.powerOps...)
}

// State returns a VM's power state by name ("" if unknown).
func (d *Driver) State(name string) hypervisor.VMState {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.vms {
		if r.vm.Name == name {
			return r.vm.State
		}
	}
	return ""
}

// Datastore returns the datastore a VM was placed on (the "datastore"
// placement key recorded at create time), or "" if unknown.
func (d *Driver) Datastore(name string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.vms {
		if r.vm.Name == name {
			return r.placement["datastore"]
		}
	}
	return ""
}
