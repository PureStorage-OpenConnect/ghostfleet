// Package hypervisor defines the platform-neutral plugin interface (HYP-1).
// Drivers translate it to a concrete API: vSphere now, later ESXi-native,
// Nutanix AHV, Proxmox VE, Hyper-V (HYP-3). The interface is deliberately
// small and capability-oriented so it doesn't bake in vSphere-isms.
package hypervisor

import (
	"context"
	"fmt"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

// Info describes the connected platform, returned by connection validation.
type Info struct {
	Product string `json:"product"` // e.g. "VMware vCenter Server"
	Version string `json:"version"` // e.g. "8.0.3"
}

// PlacementOptions enumerates the compute/storage choices offered in the
// deploy-time placement picker (PRO-9). Values are names/paths the driver
// can resolve again in VMSpec.Placement.
type PlacementOptions struct {
	Datacenters   []string          `json:"datacenters,omitempty"`
	Clusters      []string          `json:"clusters"`
	Hosts         []HostOption      `json:"hosts,omitempty"`         // for host-pinned placement
	ResourcePools []string          `json:"resourcePools,omitempty"` // inventory paths
	Datastores    []DatastoreOption `json:"datastores"`
	Networks      []string          `json:"networks"`
	Folders       []string          `json:"folders,omitempty"` // VM folder inventory paths
}

// HostOption is a selectable host plus the cluster (compute resource) it
// belongs to, so the host-pinning picker can be narrowed to the selected
// cluster (standalone hosts carry their own compute-resource name).
type HostOption struct {
	Name    string `json:"name"`
	Cluster string `json:"cluster"`
}

// DatastoreOption is a selectable datastore plus its capacity, so the picker
// can show free space and fill grade (PRO-9).
type DatastoreOption struct {
	Name          string `json:"name"`
	FreeBytes     int64  `json:"freeBytes"`
	CapacityBytes int64  `json:"capacityBytes"`
}

// DiskSpec describes one data disk to create.
type DiskSpec struct {
	SizeGiB int
	Thin    bool
}

// VMSpec is the platform-neutral description of one generated VM. Firmware
// is always UEFI (decided, DD-3) — drivers must enforce it.
type VMSpec struct {
	Name      string
	VCPUs     int
	MemoryMiB int
	Disks     []DiskSpec
	Tag       string // attached to the VM (PRO-3); empty = no tag
	// DeploymentID/Name stamp the owning deployment into the VM annotation so
	// a VM can be traced back to its deployment in the hypervisor UI.
	DeploymentID   string
	DeploymentName string
	// Placement keys (driver-specific subset of): datacenter, cluster,
	// datastore, network, folder.
	Placement model.Placement
}

// VMState is the coarse power state of a managed VM.
type VMState string

const (
	StatePoweredOn  VMState = "poweredOn"
	StatePoweredOff VMState = "poweredOff"
)

// VM is the driver's view of an existing managed VM.
type VM struct {
	Ref       string // driver-specific stable reference (e.g. vSphere moref)
	Name      string
	State     VMState
	DiskCount int
	MAC       string // primary NIC MAC, lower-case; empty if no NIC
	// OwnerID/OwnerName are parsed from the GhostFleet annotation, identifying
	// the deployment that created the VM (empty if not a GhostFleet VM or
	// unstamped). Used to detect cross-deployment conflicts.
	OwnerID   string
	OwnerName string
}

// Driver is implemented per platform. All calls must be safe to retry —
// the reconciler re-runs interrupted jobs (NFR-3).
type Driver interface {
	// Info validates the connection and reports product/version.
	Info(ctx context.Context) (Info, error)
	// ListPlacement enumerates deploy-time placement options.
	ListPlacement(ctx context.Context) (PlacementOptions, error)
	// CreateVM creates a powered-off UEFI VM and returns its reference.
	CreateVM(ctx context.Context, spec VMSpec) (string, error)
	// AddDisks appends data disks to an existing VM.
	AddDisks(ctx context.Context, ref string, disks []DiskSpec) error
	// ExtendDisks grows every data disk smaller than sizeGiB to sizeGiB.
	ExtendDisks(ctx context.Context, ref string, sizeGiB int) error
	// GetVM returns the VM, or nil if it no longer exists.
	GetVM(ctx context.Context, ref string) (*VM, error)
	// FindVMs returns the VMs among names that already exist under the
	// placement's folder, each with its owner (from the GhostFleet annotation)
	// so callers can detect cross-deployment conflicts. Names not present are
	// omitted.
	FindVMs(ctx context.Context, placement model.Placement, names []string) ([]VM, error)
	// FindVMByMAC returns the VM whose primary NIC carries the MAC (lower-case
	// match), or nil if none does. Used to locate a discovered VM — known only
	// by the MAC it PXE-booted with — for adoption. May scan the whole
	// inventory, so callers should treat it as a slow path.
	FindVMByMAC(ctx context.Context, mac string) (*VM, error)
	PowerOn(ctx context.Context, ref string) error
	PowerOff(ctx context.Context, ref string) error
	// ReadConsole returns the tail of the VM's serial console log (the
	// OS-less temp OS's only output channel). Empty if not available yet.
	ReadConsole(ctx context.Context, ref string) (string, error)
	// DeleteVM powers off (if needed) and destroys the VM with its disks.
	// Deleting a VM that is already gone is not an error.
	DeleteVM(ctx context.Context, ref string) error
	Close()
}

// Factory opens a driver for a connection. The secret is the decrypted
// credential from the store.
type Factory func(ctx context.Context, conn *model.Connection, secret string) (Driver, error)

// Registry maps plugin names (model.Connection.Plugin) to factories.
type Registry map[string]Factory

// Open looks up the plugin and opens a driver.
func (r Registry) Open(ctx context.Context, conn *model.Connection, secret string) (Driver, error) {
	f, ok := r[conn.Plugin]
	if !ok {
		return nil, fmt.Errorf("unknown hypervisor plugin %q", conn.Plugin)
	}
	return f(ctx, conn, secret)
}
