package vsphere

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/simulator"
	_ "github.com/vmware/govmomi/vapi/simulator" // registers vAPI (tags) endpoints
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/hypervisor"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

func vmRef(ref string) types.ManagedObjectReference {
	return types.ManagedObjectReference{Type: "VirtualMachine", Value: ref}
}

// withDriver runs fn against a fresh vcsim instance.
func withDriver(t *testing.T, fn func(ctx context.Context, d hypervisor.Driver)) {
	t.Helper()
	simulator.Test(func(ctx context.Context, vc *vim25.Client) {
		conn := &model.Connection{
			Plugin:      "vsphere",
			Endpoint:    vc.URL().String(),
			Username:    "user",
			InsecureTLS: true,
		}
		d, err := New(ctx, conn, "pass")
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer d.Close()
		fn(ctx, d)
	})
}

func testSpec(name string) hypervisor.VMSpec {
	return hypervisor.VMSpec{
		Name:      name,
		VCPUs:     2,
		MemoryMiB: 2048,
		Disks: []hypervisor.DiskSpec{
			{SizeGiB: 10, Thin: true},
			{SizeGiB: 20, Thin: true},
		},
		Tag: "backup=ghostfleet-test", // key=value: category "backup", tag "ghostfleet-test"
		Placement: model.Placement{
			"cluster":   "DC0_C0",
			"datastore": "LocalDS_0",
			"network":   "VM Network",
		},
	}
}

func TestInfo(t *testing.T) {
	withDriver(t, func(ctx context.Context, d hypervisor.Driver) {
		info, err := d.Info(ctx)
		if err != nil {
			t.Fatalf("Info: %v", err)
		}
		if info.Product == "" || info.Version == "" {
			t.Fatalf("empty info: %+v", info)
		}
	})
}

func TestListPlacement(t *testing.T) {
	withDriver(t, func(ctx context.Context, d hypervisor.Driver) {
		opts, err := d.ListPlacement(ctx)
		if err != nil {
			t.Fatalf("ListPlacement: %v", err)
		}
		if len(opts.Datacenters) == 0 || len(opts.Clusters) == 0 ||
			len(opts.ResourcePools) == 0 || len(opts.Datastores) == 0 ||
			len(opts.Networks) == 0 || len(opts.Folders) == 0 {
			t.Fatalf("incomplete placement options: %+v", opts)
		}
		// Folders must be inventory paths under the datacenter's vm root.
		for _, f := range opts.Folders {
			if !strings.Contains(f, "/vm") {
				t.Fatalf("folder %q is not a vm inventory path", f)
			}
		}
		// Hosts must carry their cluster so the UI can narrow the pin-VMs
		// picker to the selected cluster (vcsim: cluster DC0_C0 has hosts).
		clustered := 0
		for _, h := range opts.Hosts {
			if h.Name == "" || h.Cluster == "" {
				t.Fatalf("host option missing name or cluster: %+v", h)
			}
			if h.Cluster == "DC0_C0" {
				clustered++
			}
		}
		if clustered == 0 {
			t.Fatalf("no host mapped to cluster DC0_C0: %+v", opts.Hosts)
		}
	})
}

func TestCreateVMPinnedToHost(t *testing.T) {
	withDriver(t, func(ctx context.Context, d hypervisor.Driver) {
		spec := testSpec("ghost-pinned")
		spec.Placement = model.Placement{
			"host":      "DC0_C0_H0",
			"datastore": "LocalDS_0",
			"network":   "VM Network",
		}
		ref, err := d.CreateVM(ctx, spec)
		if err != nil {
			t.Fatalf("CreateVM pinned to host: %v", err)
		}
		vm, err := d.GetVM(ctx, ref)
		if err != nil || vm == nil || vm.Name != "ghost-pinned" {
			t.Fatalf("GetVM: %v %v", vm, err)
		}
	})
}

func TestCreateVMInResourcePool(t *testing.T) {
	withDriver(t, func(ctx context.Context, d hypervisor.Driver) {
		spec := testSpec("ghost-pooled")
		spec.Placement = model.Placement{
			"resourcePool": "/DC0/host/DC0_C0/Resources",
			"datastore":    "LocalDS_0",
			"network":      "VM Network",
		}
		ref, err := d.CreateVM(ctx, spec)
		if err != nil {
			t.Fatalf("CreateVM in resource pool: %v", err)
		}
		vm, err := d.GetVM(ctx, ref)
		if err != nil || vm == nil || vm.Name != "ghost-pooled" {
			t.Fatalf("GetVM: %v %v", vm, err)
		}
	})
}

func TestCreateGetDeleteVM(t *testing.T) {
	withDriver(t, func(ctx context.Context, d hypervisor.Driver) {
		ref, err := d.CreateVM(ctx, testSpec("ghost-0001"))
		if err != nil {
			t.Fatalf("CreateVM: %v", err)
		}

		vm, err := d.GetVM(ctx, ref)
		if err != nil || vm == nil {
			t.Fatalf("GetVM: %v, %v", vm, err)
		}
		if vm.Name != "ghost-0001" {
			t.Errorf("name = %q", vm.Name)
		}
		if vm.State != hypervisor.StatePoweredOff {
			t.Errorf("state = %q, want poweredOff", vm.State)
		}
		if vm.DiskCount != 2 {
			t.Errorf("diskCount = %d, want 2", vm.DiskCount)
		}

		if err := d.DeleteVM(ctx, ref); err != nil {
			t.Fatalf("DeleteVM: %v", err)
		}
		vm, err = d.GetVM(ctx, ref)
		if err != nil || vm != nil {
			t.Fatalf("after delete: vm=%v err=%v, want nil/nil", vm, err)
		}
		// Deleting again must be a no-op (retry safety).
		if err := d.DeleteVM(ctx, ref); err != nil {
			t.Fatalf("second DeleteVM: %v", err)
		}
	})
}

func TestUEFIFirmware(t *testing.T) {
	simulator.Test(func(ctx context.Context, vc *vim25.Client) {
		conn := &model.Connection{Plugin: "vsphere", Endpoint: vc.URL().String(), Username: "u", InsecureTLS: true}
		d, err := New(ctx, conn, "p")
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()

		ref, err := d.CreateVM(ctx, testSpec("ghost-uefi"))
		if err != nil {
			t.Fatalf("CreateVM: %v", err)
		}
		// Inspect the simulator's VM object directly.
		obj := simulator.Map(ctx).Get(vmRef(ref))
		simVM, ok := obj.(*simulator.VirtualMachine)
		if !ok {
			t.Fatalf("unexpected object type %T", obj)
		}
		if simVM.Config.Firmware != "efi" {
			t.Fatalf("firmware = %q, want efi (required for IPv6 PXE)", simVM.Config.Firmware)
		}
		if simVM.Config.BootOptions == nil || simVM.Config.BootOptions.NetworkBootProtocol != "ipv6" {
			t.Fatalf("networkBootProtocol not ipv6: %+v", simVM.Config.BootOptions)
		}
	})
}

func TestPowerCycle(t *testing.T) {
	withDriver(t, func(ctx context.Context, d hypervisor.Driver) {
		ref, err := d.CreateVM(ctx, testSpec("ghost-power"))
		if err != nil {
			t.Fatalf("CreateVM: %v", err)
		}
		if err := d.PowerOn(ctx, ref); err != nil {
			t.Fatalf("PowerOn: %v", err)
		}
		vm, _ := d.GetVM(ctx, ref)
		if vm.State != hypervisor.StatePoweredOn {
			t.Fatalf("state after PowerOn = %q", vm.State)
		}
		if err := d.PowerOff(ctx, ref); err != nil {
			t.Fatalf("PowerOff: %v", err)
		}
		vm, _ = d.GetVM(ctx, ref)
		if vm.State != hypervisor.StatePoweredOff {
			t.Fatalf("state after PowerOff = %q", vm.State)
		}
		// Deleting a powered-on VM must work too (teardown path).
		d.PowerOn(ctx, ref)
		if err := d.DeleteVM(ctx, ref); err != nil {
			t.Fatalf("DeleteVM while on: %v", err)
		}
	})
}

func TestAddAndExtendDisks(t *testing.T) {
	withDriver(t, func(ctx context.Context, d hypervisor.Driver) {
		ref, err := d.CreateVM(ctx, testSpec("ghost-disks"))
		if err != nil {
			t.Fatalf("CreateVM: %v", err)
		}

		if err := d.AddDisks(ctx, ref, []hypervisor.DiskSpec{{SizeGiB: 30, Thin: true}}); err != nil {
			t.Fatalf("AddDisks: %v", err)
		}
		vm, _ := d.GetVM(ctx, ref)
		if vm.DiskCount != 3 {
			t.Fatalf("diskCount after add = %d, want 3", vm.DiskCount)
		}

		if err := d.ExtendDisks(ctx, ref, 50); err != nil {
			t.Fatalf("ExtendDisks: %v", err)
		}
	})
}

func TestTagAssigned(t *testing.T) {
	withDriver(t, func(ctx context.Context, d hypervisor.Driver) {
		if _, err := d.CreateVM(ctx, testSpec("ghost-tagged")); err != nil {
			t.Fatalf("CreateVM with tag: %v", err)
		}
		// Second VM with the same tag exercises the get-instead-of-create path.
		spec := testSpec("ghost-tagged-2")
		if _, err := d.CreateVM(ctx, spec); err != nil {
			t.Fatalf("second CreateVM with tag: %v", err)
		}
	})
}

// TestCreateVMAdoptsExisting covers retry-safety: a second CreateVM for a name
// that already exists on the hypervisor (e.g. a prior attempt that failed after
// create) must adopt the existing VM and return its ref, not error.
func TestListPlacementReportsDatastoreCapacity(t *testing.T) {
	withDriver(t, func(ctx context.Context, d hypervisor.Driver) {
		opts, err := d.ListPlacement(ctx)
		if err != nil {
			t.Fatalf("ListPlacement: %v", err)
		}
		if len(opts.Datastores) == 0 {
			t.Fatal("no datastores reported")
		}
		for _, ds := range opts.Datastores {
			if ds.Name == "" || ds.CapacityBytes <= 0 || ds.FreeBytes <= 0 {
				t.Fatalf("datastore missing capacity: %+v", ds)
			}
		}
	})
}

func TestCreateVMStampsDeploymentAnnotation(t *testing.T) {
	withDriver(t, func(ctx context.Context, d hypervisor.Driver) {
		spec := testSpec("ghost-owned")
		spec.DeploymentID = "dep-abc123"
		spec.DeploymentName = "My Deployment"
		ref, err := d.CreateVM(ctx, spec)
		if err != nil {
			t.Fatalf("CreateVM: %v", err)
		}
		obj := simulator.Map(ctx).Get(vmRef(ref))
		simVM := obj.(*simulator.VirtualMachine)
		if !strings.Contains(simVM.Config.Annotation, "dep-abc123") ||
			!strings.Contains(simVM.Config.Annotation, "My Deployment") {
			t.Fatalf("annotation missing owner: %q", simVM.Config.Annotation)
		}
	})
}

func TestCreateVMAdoptsExisting(t *testing.T) {
	withDriver(t, func(ctx context.Context, d hypervisor.Driver) {
		ref1, err := d.CreateVM(ctx, testSpec("ghost-adopt"))
		if err != nil {
			t.Fatalf("first CreateVM: %v", err)
		}
		ref2, err := d.CreateVM(ctx, testSpec("ghost-adopt"))
		if err != nil {
			t.Fatalf("second CreateVM should adopt, got: %v", err)
		}
		if ref1 != ref2 {
			t.Fatalf("adopted ref %q != original %q", ref2, ref1)
		}
	})
}

// TestFindVMsReportsOwner verifies the real driver reads the owning deployment
// back out of the VM annotation (used for cross-deployment conflict detection).
func TestFindVMsReportsOwner(t *testing.T) {
	withDriver(t, func(ctx context.Context, d hypervisor.Driver) {
		mk := func(name, owner string) {
			s := testSpec(name)
			s.DeploymentID, s.DeploymentName = owner, owner+"-name"
			if _, err := d.CreateVM(ctx, s); err != nil {
				t.Fatalf("CreateVM %s: %v", name, err)
			}
		}
		mk("ghost-0001", "dep-A")
		mk("ghost-0002", "dep-B")

		found, err := d.FindVMs(ctx, testSpec("x").Placement,
			[]string{"ghost-0001", "ghost-0002", "ghost-0404"})
		if err != nil {
			t.Fatalf("FindVMs: %v", err)
		}
		owners := map[string]string{}
		for _, vm := range found {
			owners[vm.Name] = vm.OwnerID
		}
		if owners["ghost-0001"] != "dep-A" || owners["ghost-0002"] != "dep-B" {
			t.Fatalf("owners = %v", owners)
		}
		if _, ok := owners["ghost-0404"]; ok {
			t.Fatalf("ghost-0404 does not exist and must not be reported")
		}
	})
}

// TestFindVMsMultiDatastore guards against the regression where a
// multi-datastore deployment (datastores stored newline-separated, PRO-9)
// failed conflict detection: FindVMs passes the raw placement, and
// resolvePlacement tried to resolve the whole joined string as one datastore
// name. It must normalize and resolve only the first.
func TestFindVMsMultiDatastore(t *testing.T) {
	withDriver(t, func(ctx context.Context, d hypervisor.Driver) {
		if _, err := d.CreateVM(ctx, testSpec("ghost-0001")); err != nil {
			t.Fatalf("CreateVM: %v", err)
		}
		placement := testSpec("x").Placement
		placement["datastore"] = "LocalDS_0\nNope-DS-1\nNope-DS-2"
		found, err := d.FindVMs(ctx, placement, []string{"ghost-0001"})
		if err != nil {
			t.Fatalf("FindVMs with multi-datastore placement: %v", err)
		}
		if len(found) != 1 || found[0].Name != "ghost-0001" {
			t.Fatalf("found = %+v", found)
		}
	})
}

// TestConcurrentTagCreation mirrors the orchestrator: several VMs created in
// parallel that all need the same not-yet-existing tag. They must not race to
// create the category/tag (which yielded a 400 ALREADY_EXISTS before tagMu).
func TestConcurrentTagCreation(t *testing.T) {
	withDriver(t, func(ctx context.Context, d hypervisor.Driver) {
		const n = 8
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, errs[i] = d.CreateVM(ctx, testSpec(fmt.Sprintf("ghost-race-%02d", i)))
			}(i)
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("concurrent CreateVM %d: %v", i, err)
			}
		}
	})
}

// TestTailTextReturnsEnd pins the property the console viewer depends on: the
// text returned is the *end* of the log (reading the head instead left the UI
// showing a stale window once a log outgrew the read cap).
func TestTailTextReturnsEnd(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&b, "line %04d\n", i)
	}
	got, err := tailText(strings.NewReader(b.String()), 200)
	if err != nil {
		t.Fatalf("tailText: %v", err)
	}
	if len(got) > 200 {
		t.Fatalf("len(got) = %d, want <= 200", len(got))
	}
	if !strings.HasSuffix(got, "line 4999\n") {
		t.Fatalf("got tail %q, want the last line", got)
	}
}

// TestTailTextStripsNULs covers the NUL padding serial logs carry, including
// a fully padded tail (which must not swallow the real output).
func TestTailTextStripsNULs(t *testing.T) {
	raw := "boot\x00\x00 ok\n" + strings.Repeat("\x00", 4096)
	got, err := tailText(strings.NewReader(raw), 64)
	if err != nil {
		t.Fatalf("tailText: %v", err)
	}
	if got != "boot ok\n" {
		t.Fatalf("got %q, want %q", got, "boot ok\n")
	}
}

// TestReadConsoleFallsBackPastNULPadding covers the case that made the viewer
// claim "no console output yet" on a VM with plenty of history: the serial
// log's trailing NUL padding filled the whole suffix-Range window, so the
// ranged read transferred nothing but padding and the real output — sitting
// before that window — was never downloaded. tailText cannot recover what was
// not fetched, so ReadConsole re-reads the file unranged.
func TestReadConsoleFallsBackPastNULPadding(t *testing.T) {
	withDriver(t, func(ctx context.Context, d hypervisor.Driver) {
		ref, err := d.CreateVM(ctx, testSpec("gf-console"))
		if err != nil {
			t.Fatalf("CreateVM: %v", err)
		}
		// The console log the serial port points at: real output followed by
		// more NUL padding than one tail window holds.
		body := "[    0.00] Linux version 6.12 boot one\nagent: hello\n" +
			strings.Repeat("\x00", consoleTailBytes+4096)
		writeDatastoreFile(t, ctx, d, "gf-console/console.log", body)

		got, err := d.ReadConsole(ctx, ref)
		if err != nil {
			t.Fatalf("ReadConsole: %v", err)
		}
		if !strings.Contains(got, "agent: hello") {
			t.Fatalf("console output lost behind NUL padding: got %q", got)
		}
	})
}

// TestReadConsoleMissingLogIsEmpty keeps "the VM has never booted" reported as
// empty rather than as an error.
func TestReadConsoleMissingLogIsEmpty(t *testing.T) {
	withDriver(t, func(ctx context.Context, d hypervisor.Driver) {
		ref, err := d.CreateVM(ctx, testSpec("gf-noconsole"))
		if err != nil {
			t.Fatalf("CreateVM: %v", err)
		}
		got, err := d.ReadConsole(ctx, ref)
		if err != nil {
			t.Fatalf("ReadConsole on a VM with no log: %v", err)
		}
		if got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})
}

// writeDatastoreFile stages a file in vcsim's local datastore directory, so a
// test can have ReadConsole fetch it back over the datastore HTTP path.
func writeDatastoreFile(t *testing.T, ctx context.Context, d hypervisor.Driver, relPath, content string) {
	t.Helper()
	dv, ok := d.(*driver)
	if !ok {
		t.Fatalf("driver is %T, want *driver", d)
	}
	f := find.NewFinder(dv.client.Client, false)
	dc, err := f.DefaultDatacenter(ctx)
	if err != nil {
		t.Fatalf("finding datacenter: %v", err)
	}
	f.SetDatacenter(dc)
	ds, err := f.Datastore(ctx, "LocalDS_0")
	if err != nil {
		t.Fatalf("finding datastore: %v", err)
	}
	var mds mo.Datastore
	if err := ds.Properties(ctx, ds.Reference(), []string{"info"}, &mds); err != nil {
		t.Fatalf("datastore properties: %v", err)
	}
	dir := strings.TrimPrefix(mds.Info.GetDatastoreInfo().Url, "file://")
	path := filepath.Join(dir, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
