// Package vsphere implements the hypervisor driver for VMware vCenter
// (vSphere 8+) via govmomi. Generated VMs are OS-less UEFI shells: pvscsi
// controller, vmxnet3 NIC, N data disks, no OS disk (ARCHITECTURE.md §3).
package vsphere

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/task"
	"github.com/vmware/govmomi/vapi/rest"
	"github.com/vmware/govmomi/vapi/tags"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/hypervisor"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

// tagCategory is the vSphere tag category all GhostFleet tags live in.
const tagCategory = "ghostfleet"

// defaultFolder is the VM folder created for generated VMs when the
// placement does not name one.
const defaultFolder = "ghostfleet"

type driver struct {
	client *govmomi.Client
	user   *url.Userinfo // for the lazy vAPI/REST login (tags only)

	// restMu guards lazy login of the REST client; tagMu serializes
	// get-or-create of tag categories/tags. CreateVM runs in parallel and the
	// first VMs of a deployment all need the same (not-yet-existing) tag, so
	// without tagMu they race and all but one get a 400 ALREADY_EXISTS.
	restMu sync.Mutex
	rest   *rest.Client // nil until first tag operation (see restClient)
	tagMu  sync.Mutex
}

// New opens a vSphere driver. conn.Endpoint may be a bare host or URL; the
// /sdk path is added automatically. Only the SOAP client logs in here; the
// vAPI/REST client (needed solely for tagging) logs in lazily on first tag
// use — a full extra login per driver-open otherwise slowed every operation
// (notably the per-poll console reads, which hung during a fill when vCenter
// was contended).
func New(ctx context.Context, conn *model.Connection, secret string) (hypervisor.Driver, error) {
	u, err := soap.ParseURL(conn.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("parsing endpoint: %w", err)
	}
	u.User = url.UserPassword(conn.Username, secret)

	client, err := govmomi.NewClient(ctx, u, conn.InsecureTLS)
	if err != nil {
		return nil, fmt.Errorf("connecting to vCenter: %w", err)
	}
	return &driver{client: client, user: u.User}, nil
}

// restClient returns the vAPI/REST client, logging in on first use. Only tag
// operations need it, so non-tag drivers never pay the login cost.
func (d *driver) restClient(ctx context.Context) (*rest.Client, error) {
	d.restMu.Lock()
	defer d.restMu.Unlock()
	if d.rest != nil {
		return d.rest, nil
	}
	rc := rest.NewClient(d.client.Client)
	if err := rc.Login(ctx, d.user); err != nil {
		return nil, fmt.Errorf("vAPI login (needed for tags): %w", err)
	}
	d.rest = rc
	return rc, nil
}

func (d *driver) Close() {
	ctx := context.Background()
	d.restMu.Lock()
	if d.rest != nil {
		d.rest.Logout(ctx)
	}
	d.restMu.Unlock()
	d.client.Logout(ctx)
}

func (d *driver) Info(ctx context.Context) (hypervisor.Info, error) {
	about := d.client.ServiceContent.About
	return hypervisor.Info{Product: about.FullName, Version: about.Version}, nil
}

// finder returns a Finder scoped to the placement's datacenter (or the
// default datacenter if none is named).
func (d *driver) finder(ctx context.Context, placement model.Placement) (*find.Finder, error) {
	f := find.NewFinder(d.client.Client)
	var dc *object.Datacenter
	var err error
	if name := placement["datacenter"]; name != "" {
		dc, err = f.Datacenter(ctx, name)
	} else {
		dc, err = f.DefaultDatacenter(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("resolving datacenter: %w", err)
	}
	f.SetDatacenter(dc)
	return f, nil
}

func (d *driver) ListPlacement(ctx context.Context) (hypervisor.PlacementOptions, error) {
	var opts hypervisor.PlacementOptions
	f := find.NewFinder(d.client.Client)

	dcs, err := f.DatacenterList(ctx, "*")
	if err != nil {
		return opts, fmt.Errorf("listing datacenters: %w", err)
	}
	for _, dc := range dcs {
		opts.Datacenters = append(opts.Datacenters, dc.Name())
		f.SetDatacenter(dc)
		// One pass over compute resources yields both the cluster list and
		// each host with the cluster it belongs to, so the UI can narrow the
		// host-pinning picker to the selected cluster. A standalone host's
		// compute resource carries the host's name, keeping the two pickers
		// consistent.
		if crs, err := f.ComputeResourceList(ctx, "*"); err == nil {
			for _, c := range crs {
				opts.Clusters = append(opts.Clusters, c.Name())
				hosts, err := c.Hosts(ctx)
				if err != nil {
					continue
				}
				for _, h := range hosts {
					name, err := h.ObjectName(ctx)
					if err != nil {
						continue
					}
					opts.Hosts = append(opts.Hosts, hypervisor.HostOption{Name: name, Cluster: c.Name()})
				}
			}
		}
		if pools, err := f.ResourcePoolList(ctx, "*"); err == nil {
			for _, p := range pools {
				opts.ResourcePools = append(opts.ResourcePools, p.InventoryPath)
			}
		}
		if dss, err := f.DatastoreList(ctx, "*"); err == nil {
			for _, ds := range dss {
				opt := hypervisor.DatastoreOption{Name: ds.Name()}
				var mds mo.Datastore
				if err := ds.Properties(ctx, ds.Reference(), []string{"summary"}, &mds); err == nil {
					opt.CapacityBytes = mds.Summary.Capacity
					opt.FreeBytes = mds.Summary.FreeSpace
				}
				opts.Datastores = append(opts.Datastores, opt)
			}
		}
		if nets, err := f.NetworkList(ctx, "*"); err == nil {
			for _, n := range nets {
				path := n.GetInventoryPath()
				opts.Networks = append(opts.Networks, path[strings.LastIndex(path, "/")+1:])
			}
		}
		if folders, err := d.listVMFolders(ctx, dc); err == nil {
			opts.Folders = append(opts.Folders, folders...)
		}
	}
	return opts, nil
}

// listVMFolders returns the inventory paths of all VM folders under a
// datacenter (e.g. /DC0/vm/team-a/ghostfleet), using a container view so
// nesting depth is irrelevant. Paths are the form CreateVM resolves directly.
func (d *driver) listVMFolders(ctx context.Context, dc *object.Datacenter) ([]string, error) {
	m := view.NewManager(d.client.Client)
	v, err := m.CreateContainerView(ctx, dc.Reference(), []string{"Folder"}, true)
	if err != nil {
		return nil, err
	}
	defer v.Destroy(ctx)

	var folders []mo.Folder
	if err := v.Retrieve(ctx, []string{"Folder"}, []string{"childType"}, &folders); err != nil {
		return nil, err
	}
	finder := find.NewFinder(d.client.Client)
	finder.SetDatacenter(dc)

	var out []string
	for _, fld := range folders {
		// Only folders that can contain VMs (excludes host/network/datastore folders).
		if !slices.Contains(fld.ChildType, "VirtualMachine") {
			continue
		}
		if p, err := find.InventoryPath(ctx, d.client.Client, fld.Reference()); err == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

// resolvePlacement turns placement names into vSphere objects.
func (d *driver) resolvePlacement(ctx context.Context, placement model.Placement) (
	*find.Finder, *object.ResourcePool, *object.HostSystem, *object.Datastore, object.NetworkReference, *object.Folder, error,
) {
	fail := func(err error) (*find.Finder, *object.ResourcePool, *object.HostSystem, *object.Datastore, object.NetworkReference, *object.Folder, error) {
		return nil, nil, nil, nil, nil, nil, err
	}
	f, err := d.finder(ctx, placement)
	if err != nil {
		return fail(err)
	}

	// Optional host pinning: lab setups with host-local standard vSwitches
	// need every generated VM on one specific host (docs/VSPHERE-SETUP.md).
	var host *object.HostSystem
	if hostName := placement["host"]; hostName != "" {
		host, err = f.HostSystem(ctx, hostName)
		if err != nil {
			return fail(fmt.Errorf("resolving host %q: %w", hostName, err))
		}
	}

	// Resource pool resolution: an explicit pool (name or inventory path)
	// wins — required for least-privilege users confined to one pool; then
	// the pinned host's root pool; then the cluster's root pool.
	var pool *object.ResourcePool
	switch {
	case placement["resourcePool"] != "":
		pool, err = f.ResourcePool(ctx, placement["resourcePool"])
		if err != nil {
			return fail(fmt.Errorf("resolving resource pool %q: %w", placement["resourcePool"], err))
		}
	case host != nil:
		pool, err = host.ResourcePool(ctx)
		if err != nil {
			return fail(fmt.Errorf("resource pool of host %q: %w", placement["host"], err))
		}
	case placement["cluster"] != "":
		cr, err := f.ComputeResource(ctx, placement["cluster"])
		if err != nil {
			return fail(fmt.Errorf("resolving cluster %q: %w", placement["cluster"], err))
		}
		pool, err = cr.ResourcePool(ctx)
		if err != nil {
			return fail(fmt.Errorf("resource pool of %q: %w", placement["cluster"], err))
		}
	default:
		pool, err = f.DefaultResourcePool(ctx)
		if err != nil {
			return fail(fmt.Errorf("resolving default resource pool: %w", err))
		}
	}

	// A multi-datastore placement stores its datastores newline-separated
	// (PRO-9). CreateVM always receives a single datastore (the orchestrator
	// pins one per VM via WithDatastore); folder-only callers like FindVMs pass
	// the raw multi-datastore placement, so normalize and resolve the first —
	// resolving the whole joined string as one name would fail.
	dss := placement.Datastores()
	if len(dss) == 0 {
		return fail(fmt.Errorf("placement: datastore is required"))
	}
	dsName := dss[0]
	ds, err := f.Datastore(ctx, dsName)
	if err != nil {
		return fail(fmt.Errorf("resolving datastore %q: %w", dsName, err))
	}

	var network object.NetworkReference
	if netName := placement["network"]; netName != "" {
		network, err = f.Network(ctx, netName)
		if err != nil {
			return fail(fmt.Errorf("resolving network %q: %w", netName, err))
		}
	}

	folder, err := d.ensureFolder(ctx, f, placement["folder"])
	if err != nil {
		return fail(err)
	}
	return f, pool, host, ds, network, folder, nil
}

// ensureFolder returns the named VM folder, creating the GhostFleet default
// folder on first use.
func (d *driver) ensureFolder(ctx context.Context, f *find.Finder, name string) (*object.Folder, error) {
	if name != "" {
		folder, err := f.Folder(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("resolving folder %q: %w", name, err)
		}
		return folder, nil
	}
	if folder, err := f.Folder(ctx, defaultFolder); err == nil {
		return folder, nil
	}
	root, err := f.DefaultFolder(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving root VM folder: %w", err)
	}
	folder, err := root.CreateFolder(ctx, defaultFolder)
	if err != nil {
		// Lost a race with a parallel create — re-resolve.
		if existing, ferr := f.Folder(ctx, defaultFolder); ferr == nil {
			return existing, nil
		}
		return nil, fmt.Errorf("creating folder %q: %w", defaultFolder, err)
	}
	return folder, nil
}

func (d *driver) CreateVM(ctx context.Context, spec hypervisor.VMSpec) (string, error) {
	_, pool, host, ds, network, folder, err := d.resolvePlacement(ctx, spec.Placement)
	if err != nil {
		return "", err
	}

	devices := object.VirtualDeviceList{}
	scsi, err := devices.CreateSCSIController("pvscsi")
	if err != nil {
		return "", err
	}
	devices = append(devices, scsi)
	controller := scsi.(types.BaseVirtualController)

	for i, disk := range spec.Disks {
		// Explicit per-disk filenames — auto-derived names collide when one
		// spec carries several disks.
		fileName := fmt.Sprintf("[%s] %s/%s_%d.vmdk", ds.Name(), spec.Name, spec.Name, i+1)
		devices = append(devices, newDisk(devices, controller, fileName, disk))
	}

	if network != nil {
		backing, err := network.EthernetCardBackingInfo(ctx)
		if err != nil {
			return "", fmt.Errorf("network backing: %w", err)
		}
		nic, err := devices.CreateEthernetCard("vmxnet3", backing)
		if err != nil {
			return "", err
		}
		devices = append(devices, nic)
	}

	// Serial port logged to a datastore file — the OS-less temp OS has no
	// other way to surface agent/kernel console output for debugging. The
	// kernel cmdline makes ttyS0 the primary console (see the boot script).
	devices = append(devices, &types.VirtualSerialPort{
		VirtualDevice: types.VirtualDevice{
			Backing: &types.VirtualSerialPortFileBackingInfo{
				VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{
					FileName: fmt.Sprintf("[%s] %s/console.log", ds.Name(), spec.Name),
				},
			},
			Connectable: &types.VirtualDeviceConnectInfo{StartConnected: true, Connected: true},
		},
		YieldOnPoll: true,
	})

	deviceChange, err := devices.ConfigSpec(types.VirtualDeviceConfigSpecOperationAdd)
	if err != nil {
		return "", err
	}

	config := types.VirtualMachineConfigSpec{
		Name:     spec.Name,
		GuestId:  string(types.VirtualMachineGuestOsIdentifierOtherGuest64),
		Firmware: string(types.GuestOsDescriptorFirmwareTypeEfi), // UEFI required for IPv6 PXE (DD-3)
		NumCPUs:  int32(spec.VCPUs),
		MemoryMB: int64(spec.MemoryMiB),
		Files: &types.VirtualMachineFileInfo{
			VmPathName: fmt.Sprintf("[%s]", ds.Name()),
		},
		Annotation:   vmAnnotation(spec),
		DeviceChange: deviceChange,
		BootOptions: &types.VirtualMachineBootOptions{
			// The EFI firmware attempts IPv4 PXE by default; the isolated
			// network is IPv6-only (NET-2), so opt into IPv6 network boot.
			NetworkBootProtocol: string(types.VirtualMachineBootOptionsNetworkBootProtocolTypeIpv6),
		},
	}

	var ref types.ManagedObjectReference
	task, err := folder.CreateVM(ctx, config, pool, host) // host may be nil (cluster/DRS decides)
	if err == nil {
		var info *types.TaskInfo
		if info, err = task.WaitForResult(ctx, nil); err == nil {
			ref = info.Result.(types.ManagedObjectReference)
		}
	}
	if isVMAlreadyExists(err) {
		// Retry-safety (NFR-3): a previous attempt created the VM but failed
		// before it was recorded (e.g. the tag step), so the orchestrator
		// re-requests it. Adopt the existing VM instead of failing; tagging
		// below is idempotent and heals a half-finished create.
		vm, ferr := findVMByName(ctx, folder, spec.Name)
		if ferr != nil {
			return "", fmt.Errorf("adopting existing VM %q: %w", spec.Name, ferr)
		}
		ref = vm.Reference()
		// Re-stamp ownership so an adopted VM (recovery or a deliberate
		// cross-deployment adopt) reflects its new deployment in the annotation.
		if task, rerr := vm.Reconfigure(ctx, types.VirtualMachineConfigSpec{Annotation: vmAnnotation(spec)}); rerr == nil {
			task.WaitForResult(ctx, nil)
		}
	} else if err != nil {
		return "", fmt.Errorf("creating VM %q: %w", spec.Name, err)
	}

	if spec.Tag != "" {
		if err := d.assignTag(ctx, ref, spec.Tag); err != nil {
			return ref.Value, fmt.Errorf("tagging VM %q: %w", spec.Name, err)
		}
	}
	return ref.Value, nil
}

// newDisk builds a new thin/thick data disk backed by fileName, keyed to fit
// the device list (unit number assignment happens via the device list).
func newDisk(devices object.VirtualDeviceList, controller types.BaseVirtualController, fileName string, spec hypervisor.DiskSpec) *types.VirtualDisk {
	disk := &types.VirtualDisk{
		VirtualDevice: types.VirtualDevice{
			Backing: &types.VirtualDiskFlatVer2BackingInfo{
				DiskMode:        string(types.VirtualDiskModePersistent),
				ThinProvisioned: types.NewBool(spec.Thin),
				VirtualDeviceFileBackingInfo: types.VirtualDeviceFileBackingInfo{
					FileName: fileName,
				},
			},
		},
		CapacityInKB: int64(spec.SizeGiB) * 1024 * 1024,
	}
	devices.AssignController(disk, controller)
	disk.VirtualDevice.Key = devices.NewKey()
	return disk
}

func (d *driver) vm(ref string) *object.VirtualMachine {
	return object.NewVirtualMachine(d.client.Client,
		types.ManagedObjectReference{Type: "VirtualMachine", Value: ref})
}

func (d *driver) AddDisks(ctx context.Context, ref string, disks []hypervisor.DiskSpec) error {
	vm := d.vm(ref)
	devices, err := vm.Device(ctx)
	if err != nil {
		return fmt.Errorf("reading devices: %w", err)
	}
	controllers := devices.SelectByType((*types.ParaVirtualSCSIController)(nil))
	if len(controllers) == 0 {
		return fmt.Errorf("vm %s has no pvscsi controller", ref)
	}
	controller := controllers[0].(types.BaseVirtualController)

	// The datastore of the VM's existing files hosts the new disks too.
	var mvm mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), []string{"name", "datastore"}, &mvm); err != nil {
		return fmt.Errorf("resolving VM datastore: %w", err)
	}
	if len(mvm.Datastore) == 0 {
		return fmt.Errorf("vm %s has no datastore", ref)
	}
	dsName, err := object.NewDatastore(vm.Client(), mvm.Datastore[0]).ObjectName(ctx)
	if err != nil {
		return fmt.Errorf("datastore name: %w", err)
	}

	existing := len(devices.SelectByType((*types.VirtualDisk)(nil)))
	var change []types.BaseVirtualDeviceConfigSpec
	for i, spec := range disks {
		fileName := fmt.Sprintf("[%s] %s/%s_%d.vmdk", dsName, mvm.Name, mvm.Name, existing+i+1)
		disk := newDisk(devices, controller, fileName, spec)
		devices = append(devices, disk)
		change = append(change, &types.VirtualDeviceConfigSpec{
			Operation:     types.VirtualDeviceConfigSpecOperationAdd,
			FileOperation: types.VirtualDeviceConfigSpecFileOperationCreate,
			Device:        disk,
		})
	}
	task, err := vm.Reconfigure(ctx, types.VirtualMachineConfigSpec{DeviceChange: change})
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

func (d *driver) ExtendDisks(ctx context.Context, ref string, sizeGiB int) error {
	vm := d.vm(ref)
	devices, err := vm.Device(ctx)
	if err != nil {
		return fmt.Errorf("reading devices: %w", err)
	}
	target := int64(sizeGiB) * 1024 * 1024
	var change []types.BaseVirtualDeviceConfigSpec
	for _, dev := range devices.SelectByType((*types.VirtualDisk)(nil)) {
		disk := dev.(*types.VirtualDisk)
		if disk.CapacityInKB < target {
			disk.CapacityInKB = target
			change = append(change, &types.VirtualDeviceConfigSpec{
				Operation: types.VirtualDeviceConfigSpecOperationEdit,
				Device:    disk,
			})
		}
	}
	if len(change) == 0 {
		return nil
	}
	task, err := vm.Reconfigure(ctx, types.VirtualMachineConfigSpec{DeviceChange: change})
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

func (d *driver) GetVM(ctx context.Context, ref string) (*hypervisor.VM, error) {
	vm := d.vm(ref)
	var props mo.VirtualMachine
	err := vm.Properties(ctx, vm.Reference(), []string{"name", "runtime", "config"}, &props)
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	state := hypervisor.StatePoweredOff
	if props.Runtime.PowerState == types.VirtualMachinePowerStatePoweredOn {
		state = hypervisor.StatePoweredOn
	}
	diskCount := 0
	mac := ""
	annotation := ""
	if props.Config != nil {
		annotation = props.Config.Annotation
		for _, dev := range props.Config.Hardware.Device {
			if _, ok := dev.(*types.VirtualDisk); ok {
				diskCount++
			}
			if nic, ok := dev.(types.BaseVirtualEthernetCard); ok && mac == "" {
				mac = strings.ToLower(nic.GetVirtualEthernetCard().MacAddress)
			}
		}
	}
	ownerID, ownerName := parseOwner(annotation)
	return &hypervisor.VM{
		Ref: ref, Name: props.Name, State: state, DiskCount: diskCount, MAC: mac,
		OwnerID: ownerID, OwnerName: ownerName,
	}, nil
}

// FindVMs returns the VMs among names that exist under the placement's folder,
// with their owner parsed from the GhostFleet annotation.
func (d *driver) FindVMs(ctx context.Context, placement model.Placement, names []string) ([]hypervisor.VM, error) {
	_, _, _, _, _, folder, err := d.resolvePlacement(ctx, placement)
	if err != nil {
		return nil, err
	}
	children, err := folder.Children(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing folder: %w", err)
	}
	var refs []types.ManagedObjectReference
	for _, c := range children {
		if c.Reference().Type == "VirtualMachine" {
			refs = append(refs, c.Reference())
		}
	}
	if len(refs) == 0 {
		return nil, nil
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	var vms []mo.VirtualMachine
	pc := property.DefaultCollector(d.client.Client)
	if err := pc.Retrieve(ctx, refs, []string{"name", "config.annotation"}, &vms); err != nil {
		return nil, fmt.Errorf("reading VMs: %w", err)
	}
	var out []hypervisor.VM
	for _, vm := range vms {
		if !want[vm.Name] {
			continue
		}
		annotation := ""
		if vm.Config != nil {
			annotation = vm.Config.Annotation
		}
		ownerID, ownerName := parseOwner(annotation)
		out = append(out, hypervisor.VM{
			Ref: vm.Self.Value, Name: vm.Name, OwnerID: ownerID, OwnerName: ownerName,
		})
	}
	return out, nil
}

// FindVMByMAC scans the inventory for the VM whose primary NIC carries the
// MAC. A container view over all VMs with one property-collector retrieval
// keeps it to a single round trip, but it still touches every VM — slow path,
// used only when adopting a discovered VM.
func (d *driver) FindVMByMAC(ctx context.Context, mac string) (*hypervisor.VM, error) {
	mac = strings.ToLower(mac)
	m := view.NewManager(d.client.Client)
	v, err := m.CreateContainerView(ctx, d.client.ServiceContent.RootFolder, []string{"VirtualMachine"}, true)
	if err != nil {
		return nil, fmt.Errorf("creating VM view: %w", err)
	}
	defer v.Destroy(ctx)

	var vms []mo.VirtualMachine
	if err := v.Retrieve(ctx, []string{"VirtualMachine"},
		[]string{"name", "runtime.powerState", "config.hardware.device", "config.annotation"}, &vms); err != nil {
		return nil, fmt.Errorf("reading VMs: %w", err)
	}
	for _, vm := range vms {
		if vm.Config == nil {
			continue
		}
		diskCount, vmMAC := 0, ""
		for _, dev := range vm.Config.Hardware.Device {
			if _, ok := dev.(*types.VirtualDisk); ok {
				diskCount++
			}
			if nic, ok := dev.(types.BaseVirtualEthernetCard); ok && vmMAC == "" {
				vmMAC = strings.ToLower(nic.GetVirtualEthernetCard().MacAddress)
			}
		}
		if vmMAC != mac {
			continue
		}
		state := hypervisor.StatePoweredOff
		if vm.Runtime.PowerState == types.VirtualMachinePowerStatePoweredOn {
			state = hypervisor.StatePoweredOn
		}
		ownerID, ownerName := parseOwner(vm.Config.Annotation)
		return &hypervisor.VM{
			Ref: vm.Self.Value, Name: vm.Name, State: state, DiskCount: diskCount, MAC: vmMAC,
			OwnerID: ownerID, OwnerName: ownerName,
		}, nil
	}
	return nil, nil
}

// parseOwner extracts the owning deployment id/name from a VM annotation
// written by vmAnnotation.
func parseOwner(annotation string) (id, name string) {
	for _, line := range strings.Split(annotation, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, annotationOwnerKey+":"); ok {
			id = strings.TrimSpace(v)
		} else if v, ok := strings.CutPrefix(line, "deployment:"); ok {
			name = strings.TrimSpace(v)
		}
	}
	return id, name
}

// isNotFound reports whether err means the managed object no longer exists.
func isNotFound(err error) bool {
	if soap.IsSoapFault(err) {
		if _, ok := soap.ToSoapFault(err).VimFault().(types.ManagedObjectNotFound); ok {
			return true
		}
	}
	return strings.Contains(err.Error(), "ManagedObjectNotFound")
}

// consoleTailBytes caps how much of the console log we return — enough to
// contain several boot sessions, which the API splits apart.
const consoleTailBytes = 4 << 20

// consoleReadTimeout bounds a console read so a contended vCenter (e.g. mid-
// fill) fails fast with a clear message instead of hanging the polling UI.
const consoleReadTimeout = 12 * time.Second

// ReadConsole downloads the tail of the VM's serial-port log file from the
// datastore (the file the serial port is backed by).
func (d *driver) ReadConsole(ctx context.Context, ref string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, consoleReadTimeout)
	defer cancel()
	vm := d.vm(ref)
	var mvm mo.VirtualMachine
	if err := vm.Properties(ctx, vm.Reference(), []string{"config.hardware.device", "datastore"}, &mvm); err != nil {
		if isNotFound(err) {
			return "", nil
		}
		return "", err
	}
	// The serial port's backing filename is the console log's datastore path.
	var logPath string
	if mvm.Config != nil {
		for _, dev := range mvm.Config.Hardware.Device {
			if sp, ok := dev.(*types.VirtualSerialPort); ok {
				if b, ok := sp.Backing.(*types.VirtualSerialPortFileBackingInfo); ok {
					logPath = b.FileName
				}
			}
		}
	}
	if logPath == "" {
		return "", nil // no serial port (e.g. VM predates the feature)
	}
	var dp object.DatastorePath
	if !dp.FromString(logPath) {
		return "", fmt.Errorf("unparseable console path %q", logPath)
	}
	// Resolve the datastore object by matching the VM's datastores by name.
	for _, dsRef := range mvm.Datastore {
		ds := object.NewDatastore(d.client.Client, dsRef)
		name, err := ds.ObjectName(ctx)
		if err != nil || name != dp.Datastore {
			continue
		}
		rc, status, err := d.downloadTail(ctx, ds, dp.Path, consoleTailBytes)
		if err != nil {
			if status == http.StatusNotFound {
				return "", nil // no log file yet: the VM has never booted
			}
			// Anything else is a real failure (a contended vCenter, a bad
			// ticket). Report it — collapsing it into "" would render as "no
			// console output yet" and hide the boot picker, making the history
			// that *is* in the file unreachable.
			return "", fmt.Errorf("reading %s: %w", logPath, err)
		}
		text, err := tailText(rc, consoleTailBytes)
		rc.Close()
		if err != nil {
			return "", err
		}
		// A serial log is NUL-padded, and the padding can fill the whole tail
		// window while the real output sits before it — tailText strips the
		// NULs and we are left with nothing. Read the file unranged then;
		// tailText keeps the last n *non-NUL* bytes, so the genuine end wins.
		if strings.TrimSpace(text) == "" {
			rc, _, err := d.datastoreGet(ctx, ds, dp.Path, "")
			if err != nil {
				return "", nil // the ranged read already said "empty"
			}
			defer rc.Close()
			return tailText(rc, consoleTailBytes)
		}
		return text, nil
	}
	return "", nil
}

// downloadTail opens the last n bytes of a datastore file via a suffix Range
// request, so a log that has grown over many boots costs one bounded transfer
// per read (and, crucially, yields its *current* end). It returns the HTTP
// status alongside the body so the caller can tell "no log yet" (404) from a
// read that genuinely failed.
func (d *driver) downloadTail(ctx context.Context, ds *object.Datastore, path string, n int64) (io.ReadCloser, int, error) {
	rc, status, err := d.datastoreGet(ctx, ds, path, fmt.Sprintf("bytes=-%d", n))
	// Any refusal of the range that is not "no such file" gets one unranged
	// retry: a server that rejects a suffix range still serves the whole file
	// (416/501, but also a 400 or a proxy that mangles the header), and
	// tailText caps what we keep from it either way.
	if err != nil && status != http.StatusNotFound {
		rc, status, err = d.datastoreGet(ctx, ds, path, "")
	}
	return rc, status, err
}

// datastoreGet GETs a datastore file (optionally a byte range), returning the
// response status alongside the body so the caller can react to it. Unlike
// govmomi's Datastore.Download it accepts 206 Partial Content.
func (d *driver) datastoreGet(ctx context.Context, ds *object.Datastore, path, byteRange string) (io.ReadCloser, int, error) {
	u, ticket, err := ds.ServiceTicket(ctx, path, "GET")
	if err != nil {
		return nil, 0, err
	}
	p := soap.Download{Method: "GET"}
	if byteRange != "" {
		p.Headers = map[string]string{"Range": byteRange}
	}
	if ticket != nil {
		p.Ticket = ticket
		p.Close = true // no Keep-Alive to ESX (as govmomi's own download does)
	}
	res, err := d.client.Client.DownloadRequest(ctx, u, &p)
	if err != nil {
		return nil, 0, err
	}
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusPartialContent {
		res.Body.Close()
		return nil, res.StatusCode, fmt.Errorf("download %s: %s", path, res.Status)
	}
	return res.Body, res.StatusCode, nil
}

// tailText streams r, dropping NULs (serial logs are NUL-padded), and returns
// the last n bytes of what remains — the end of the log, not its head. It
// keeps only a rolling tail in memory, so an unranged whole-file response
// stays bounded.
func tailText(r io.Reader, n int) (string, error) {
	buf := make([]byte, 0, 2*n)
	chunk := make([]byte, 64<<10)
	for {
		m, err := r.Read(chunk)
		for _, b := range chunk[:m] {
			if b != 0 {
				buf = append(buf, b)
			}
		}
		if len(buf) > 2*n {
			buf = append(buf[:0], buf[len(buf)-n:]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if len(buf) > n {
		buf = buf[len(buf)-n:]
	}
	return string(buf), nil
}

func (d *driver) PowerOn(ctx context.Context, ref string) error {
	task, err := d.vm(ref).PowerOn(ctx)
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

func (d *driver) PowerOff(ctx context.Context, ref string) error {
	task, err := d.vm(ref).PowerOff(ctx)
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

func (d *driver) DeleteVM(ctx context.Context, ref string) error {
	vm := d.vm(ref)
	existing, err := d.GetVM(ctx, ref)
	if err != nil {
		return err
	}
	if existing == nil {
		return nil // already gone — retry-safe by contract
	}
	if existing.State == hypervisor.StatePoweredOn {
		if task, err := vm.PowerOff(ctx); err == nil {
			task.Wait(ctx) // best effort; Destroy fails loudly if still on
		}
	}
	task, err := vm.Destroy(ctx)
	if err != nil {
		return err
	}
	return task.Wait(ctx)
}

// assignTag attaches the tag (creating category/tag on first use, PRO-3).
// A plain tag name lands in the default "ghostfleet" category; "key=value"
// uses key as the category and value as the tag name — the idiomatic
// vSphere modeling for key/value pairs.
func (d *driver) assignTag(ctx context.Context, ref types.ManagedObjectReference, tag string) error {
	rc, err := d.restClient(ctx)
	if err != nil {
		return err
	}
	m := tags.NewManager(rc)

	category := tagCategory
	if key, value, found := strings.Cut(tag, "="); found {
		category, tag = key, value
	}

	tagID, err := d.ensureTagID(ctx, m, category, tag)
	if err != nil {
		return err
	}
	return m.AttachTag(ctx, tagID, ref)
}

// ensureTagID returns the ID of the (category, tag) pair, creating either on
// first use. The get-or-create is serialized (tagMu) so parallel CreateVM
// calls don't race to create the same tag; a concurrent creation that still
// slips through (e.g. another controller process, or a retry) surfaces as
// ALREADY_EXISTS, which we treat as success and re-fetch.
func (d *driver) ensureTagID(ctx context.Context, m *tags.Manager, category, tag string) (string, error) {
	d.tagMu.Lock()
	defer d.tagMu.Unlock()

	catID := ""
	if cat, err := m.GetCategory(ctx, category); err == nil {
		catID = cat.ID
	} else {
		id, err := m.CreateCategory(ctx, &tags.Category{
			Name:            category,
			Description:     "GhostFleet deployment tags",
			Cardinality:     "MULTIPLE",
			AssociableTypes: []string{"VirtualMachine"},
		})
		if isAlreadyExists(err) {
			cat, gerr := m.GetCategory(ctx, category)
			if gerr != nil {
				return "", fmt.Errorf("resolving existing tag category %q: %w", category, gerr)
			}
			id = cat.ID
		} else if err != nil {
			return "", fmt.Errorf("creating tag category %q: %w", category, err)
		}
		catID = id
	}

	if t, err := m.GetTagForCategory(ctx, tag, catID); err == nil {
		return t.ID, nil
	}
	id, err := m.CreateTag(ctx, &tags.Tag{Name: tag, CategoryID: catID})
	if isAlreadyExists(err) {
		t, gerr := m.GetTagForCategory(ctx, tag, catID)
		if gerr != nil {
			return "", fmt.Errorf("resolving existing tag %q: %w", tag, gerr)
		}
		return t.ID, nil
	}
	if err != nil {
		return "", fmt.Errorf("creating tag %q: %w", tag, err)
	}
	return id, nil
}

// isAlreadyExists reports whether err is vCenter's ALREADY_EXISTS fault, which
// a concurrent create loses to and which we can safely treat as success.
func isAlreadyExists(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already_exists")
}

// annotationOwnerKey tags a VM's owning deployment ID in its annotation, so a
// VM can be traced back to (or, later, claimed by) its deployment.
const annotationOwnerKey = "ghostfleet-deployment-id"

// vmAnnotation builds the VM notes: a human line plus machine-readable owner
// fields. Kept stable and grep-friendly for future ownership checks.
func vmAnnotation(spec hypervisor.VMSpec) string {
	a := "Generated by GhostFleet — OS-less backup source VM"
	if spec.DeploymentName != "" {
		a += "\ndeployment: " + spec.DeploymentName
	}
	if spec.DeploymentID != "" {
		a += "\n" + annotationOwnerKey + ": " + spec.DeploymentID
	}
	return a
}

// isVMAlreadyExists reports whether err means a same-named VM from a prior
// create attempt is already on the hypervisor: DuplicateName (the inventory
// name is taken) or FileAlreadyExists (its datastore files are). Either way the
// VM can be adopted rather than re-created.
func isVMAlreadyExists(err error) bool {
	var te task.Error
	if errors.As(err, &te) {
		switch te.Fault().(type) {
		case *types.DuplicateName, *types.FileAlreadyExists:
			return true
		}
	}
	return false
}

// findVMByName returns the VM with the given name directly under folder, used
// to adopt a VM left by a half-finished create. Errors if none matches.
func findVMByName(ctx context.Context, folder *object.Folder, name string) (*object.VirtualMachine, error) {
	children, err := folder.Children(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing folder: %w", err)
	}
	for _, c := range children {
		ref := c.Reference()
		if ref.Type != "VirtualMachine" {
			continue
		}
		vm := object.NewVirtualMachine(folder.Client(), ref)
		n, err := vm.ObjectName(ctx)
		if err != nil {
			return nil, err
		}
		if n == name {
			return vm, nil
		}
	}
	return nil, fmt.Errorf("not found in target folder")
}
