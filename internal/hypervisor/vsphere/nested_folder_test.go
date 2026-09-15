package vsphere

import (
	"context"
	"testing"

	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/simulator"
	"github.com/vmware/govmomi/vim25"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

// TestNestedFolderForms documents which placement["folder"] forms resolve a
// nested VM folder (docs/VSPHERE-SETUP.md): absolute inventory path,
// vm/-prefixed relative path, and a bare (unique) name all work.
func TestNestedFolderForms(t *testing.T) {
	simulator.Test(func(ctx context.Context, vc *vim25.Client) {
		// Build /DC0/vm/team-a/ghostfleet up front.
		f := find.NewFinder(vc)
		dc, err := f.DefaultDatacenter(ctx)
		if err != nil {
			t.Fatal(err)
		}
		f.SetDatacenter(dc)
		folders, err := dc.Folders(ctx)
		if err != nil {
			t.Fatal(err)
		}
		teamA, err := folders.VmFolder.CreateFolder(ctx, "team-a")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := teamA.CreateFolder(ctx, "ghostfleet"); err != nil {
			t.Fatal(err)
		}

		conn := &model.Connection{Plugin: "vsphere", Endpoint: vc.URL().String(), Username: "u", InsecureTLS: true}
		d, err := New(ctx, conn, "p")
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()

		cases := []struct {
			form string
			ok   bool
		}{
			{"/DC0/vm/team-a/ghostfleet", true}, // absolute inventory path
			{"vm/team-a/ghostfleet", true},      // relative incl. vm root
			{"ghostfleet", true},                // bare name, unique in the DC
			{"team-a/ghostfleet", false},        // relative without vm/ prefix
		}
		for i, tc := range cases {
			spec := testSpec("ghost-nest-" + string(rune('a'+i)))
			spec.Tag = ""
			spec.Placement = model.Placement{
				"cluster": "DC0_C0", "datastore": "LocalDS_0",
				"network": "VM Network", "folder": tc.form,
			}
			_, err := d.CreateVM(ctx, spec)
			if tc.ok && err != nil {
				t.Errorf("folder form %q should resolve, got: %v", tc.form, err)
			}
			if !tc.ok && err == nil {
				t.Errorf("folder form %q unexpectedly resolved", tc.form)
			}
		}
	})
}
