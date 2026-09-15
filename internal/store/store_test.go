package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testSpec() model.ProfileSpec {
	s := model.ProfileSpec{
		VMCount: 4, NamePrefix: "test", DisksPerVM: 2,
		DiskSizeGiB: 10, DataPerDiskGiB: 5,
		CompressPercent: 50, DedupePercent: 10, CrossVMDedupePercent: 10,
		ChangePercent: 5, GrowthPercent: 2,
	}
	s.ApplyDefaults()
	return s
}

func TestProfileVersioning(t *testing.T) {
	s := testStore(t)

	p, err := s.CreateProfile("alpha", testSpec())
	if err != nil {
		t.Fatalf("CreateProfile: %v", err)
	}
	if p.CurrentVersion != 1 {
		t.Fatalf("new profile version = %d, want 1", p.CurrentVersion)
	}

	spec2 := testSpec()
	spec2.VMCount = 8
	p2, err := s.UpdateProfileSpec(p.ID, spec2)
	if err != nil {
		t.Fatalf("UpdateProfileSpec: %v", err)
	}
	if p2.CurrentVersion != 2 || p2.Spec.VMCount != 8 {
		t.Fatalf("after update: version=%d vmCount=%d, want 2/8", p2.CurrentVersion, p2.Spec.VMCount)
	}

	// Old version stays immutable and retrievable.
	v1, err := s.GetProfileVersion(p.ID, 1)
	if err != nil {
		t.Fatalf("GetProfileVersion(1): %v", err)
	}
	if v1.Spec.VMCount != 4 {
		t.Fatalf("v1 vmCount = %d, want 4", v1.Spec.VMCount)
	}

	versions, err := s.ListProfileVersions(p.ID)
	if err != nil || len(versions) != 2 {
		t.Fatalf("ListProfileVersions: %v, len=%d, want 2", err, len(versions))
	}
}

func TestProfileNameConflict(t *testing.T) {
	s := testStore(t)
	if _, err := s.CreateProfile("dup", testSpec()); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateProfile("dup", testSpec())
	var conflict ErrConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
}

func TestConnectionSecretRoundTrip(t *testing.T) {
	s := testStore(t)
	c, err := s.CreateConnection(&model.Connection{
		Name: "lab-vcenter", Plugin: "vsphere",
		Endpoint: "https://vcenter.lab", Username: "administrator@vsphere.local",
	}, []byte("encrypted-blob"))
	if err != nil {
		t.Fatalf("CreateConnection: %v", err)
	}

	blob, err := s.GetConnectionSecret(c.ID)
	if err != nil || string(blob) != "encrypted-blob" {
		t.Fatalf("GetConnectionSecret: %q, %v", blob, err)
	}

	// Update without touching the secret keeps the old blob.
	c.Endpoint = "https://vcenter2.lab"
	if err := s.UpdateConnection(c, nil); err != nil {
		t.Fatalf("UpdateConnection: %v", err)
	}
	blob, _ = s.GetConnectionSecret(c.ID)
	if string(blob) != "encrypted-blob" {
		t.Fatalf("secret changed unexpectedly: %q", blob)
	}
}

func TestDeploymentLifecycle(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProfile("prof", testSpec())
	c, _ := s.CreateConnection(&model.Connection{
		Name: "vc", Plugin: "vsphere", Endpoint: "https://vc", Username: "u",
	}, []byte("x"))

	d, err := s.CreateDeployment(&model.Deployment{
		Name: "deploy-1", ProfileID: p.ID, ProfileVersion: 1,
		ConnectionID: c.ID, Spec: testSpec(),
		Placement: model.Placement{"cluster": "Cluster1", "datastore": "DS1"},
	})
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	if d.Status != model.DeploymentNew {
		t.Fatalf("status = %q, want new", d.Status)
	}

	// Profile and connection in use cannot be deleted (PRO-7 guard).
	if err := s.DeleteProfile(p.ID); err == nil {
		t.Fatal("expected conflict deleting profile in use")
	}
	if err := s.DeleteConnection(c.ID); err == nil {
		t.Fatal("expected conflict deleting connection in use")
	}

	// Runs attach to the deployment.
	if _, err := s.CreateRun(d.ID, model.RunInitialFill); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	runs, err := s.ListRuns(d.ID)
	if err != nil || len(runs) != 1 || runs[0].Type != model.RunInitialFill {
		t.Fatalf("ListRuns: %v, %+v", err, runs)
	}

	// After teardown the profile becomes deletable, run history stays (PRO-7).
	if err := s.SetDeploymentStatus(d.ID, model.DeploymentDeleted); err != nil {
		t.Fatalf("SetDeploymentStatus: %v", err)
	}
	if err := s.DeleteProfile(p.ID); err != nil {
		t.Fatalf("DeleteProfile after teardown: %v", err)
	}
	got, err := s.GetDeployment(d.ID)
	if err != nil {
		t.Fatalf("deployment should survive profile deletion: %v", err)
	}
	if got.ProfileID != "" {
		t.Fatalf("profileId should be cleared by ON DELETE SET NULL, got %q", got.ProfileID)
	}
	runs, _ = s.ListRuns(d.ID)
	if len(runs) != 1 {
		t.Fatal("run history lost after profile deletion")
	}
}

func TestDeploymentNameReuseAfterTeardown(t *testing.T) {
	s := testStore(t)
	c, _ := s.CreateConnection(&model.Connection{
		Name: "vc", Plugin: "vsphere", Endpoint: "https://vc", Username: "u",
	}, []byte("x"))
	mk := func() (*model.Deployment, error) {
		return s.CreateDeployment(&model.Deployment{
			Name: "cr-ghostfleet", ConnectionID: c.ID, Spec: testSpec(),
			Placement: model.Placement{},
		})
	}

	d1, err := mk()
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	// Same name while the first is live is rejected.
	if _, err := mk(); err == nil {
		t.Fatal("expected conflict reusing a live deployment name")
	}
	// Teardown frees the name (and keeps the record, renamed).
	if err := s.MarkDeploymentDeleted(d1.ID); err != nil {
		t.Fatalf("MarkDeploymentDeleted: %v", err)
	}
	gone, _ := s.GetDeployment(d1.ID)
	if gone.Status != model.DeploymentDeleted || !strings.Contains(gone.Name, "(deleted") {
		t.Fatalf("torn-down deployment: status=%q name=%q", gone.Status, gone.Name)
	}
	// The original name is now available again.
	if _, err := mk(); err != nil {
		t.Fatalf("reusing name after teardown should succeed: %v", err)
	}
}

func TestMigrationIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := s1.CreateProfile("keep", testSpec()); err != nil {
		t.Fatal(err)
	}
	s1.Close()

	s2, err := Open(path) // re-running migrations must not destroy data
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()
	profiles, err := s2.ListProfiles()
	if err != nil || len(profiles) != 1 {
		t.Fatalf("data lost after reopen: %v, len=%d", err, len(profiles))
	}
}

func TestManagedVMs(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProfile("prof-vms", testSpec())
	c, _ := s.CreateConnection(&model.Connection{Name: "vc-vms", Plugin: "vsphere", Endpoint: "e", Username: "u"}, []byte("x"))
	d, _ := s.CreateDeployment(&model.Deployment{
		Name: "dep-vms", ProfileID: p.ID, ProfileVersion: 1, ConnectionID: c.ID,
		Spec: testSpec(), Placement: model.Placement{},
	})

	v := &model.ManagedVM{DeploymentID: d.ID, Name: "ghost-0001", Ref: "vm-42", DiskCount: 2, DiskSizeGiB: 10}
	if err := s.AddManagedVM(v); err != nil {
		t.Fatalf("AddManagedVM: %v", err)
	}
	if err := s.UpdateManagedVMDisks(v.ID, 3, 20); err != nil {
		t.Fatalf("UpdateManagedVMDisks: %v", err)
	}
	vms, err := s.ListManagedVMs(d.ID)
	if err != nil || len(vms) != 1 || vms[0].DiskCount != 3 || vms[0].DiskSizeGiB != 20 {
		t.Fatalf("ListManagedVMs: %v %+v", err, vms)
	}
	if err := s.DeleteManagedVM(v.ID); err != nil {
		t.Fatalf("DeleteManagedVM: %v", err)
	}
	vms, _ = s.ListManagedVMs(d.ID)
	if len(vms) != 0 {
		t.Fatalf("expected no VMs, got %d", len(vms))
	}
}

func TestRunLifecycle(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProfile("prof-run", testSpec())
	c, _ := s.CreateConnection(&model.Connection{Name: "vc-run", Plugin: "vsphere", Endpoint: "e", Username: "u"}, []byte("x"))
	d, _ := s.CreateDeployment(&model.Deployment{
		Name: "dep-run", ProfileID: p.ID, ProfileVersion: 1, ConnectionID: c.ID,
		Spec: testSpec(), Placement: model.Placement{},
	})

	r, err := s.CreateRun(d.ID, model.RunDeploy)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := s.StartRun(r.ID); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if err := s.FinishRun(r.ID, model.RunSucceeded, `{"vmsCreated":4}`); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	runs, _ := s.ListRuns(d.ID)
	if len(runs) != 1 || runs[0].Status != model.RunSucceeded || runs[0].StartedAt == nil || runs[0].FinishedAt == nil {
		t.Fatalf("run after finish: %+v", runs[0])
	}
}

func TestSchedules(t *testing.T) {
	s := testStore(t)
	p, _ := s.CreateProfile("prof-sch", testSpec())
	c, _ := s.CreateConnection(&model.Connection{Name: "vc-sch", Plugin: "vsphere", Endpoint: "e", Username: "u"}, []byte("x"))
	d, _ := s.CreateDeployment(&model.Deployment{
		Name: "dep-sch", ProfileID: p.ID, ProfileVersion: 1, ConnectionID: c.ID,
		Spec: testSpec(), Placement: model.Placement{},
	})

	next := time.Now().UTC().Truncate(time.Second).Add(-time.Minute) // already due
	sc, err := s.CreateSchedule(&model.Schedule{
		DeploymentID: d.ID, Action: model.RunIncremental,
		Kind: model.ScheduleEvery, Spec: "24h", Enabled: true, NextRunAt: &next,
	})
	if err != nil {
		t.Fatalf("CreateSchedule: %v", err)
	}

	got, err := s.GetSchedule(sc.ID)
	if err != nil || got.Action != model.RunIncremental || got.NextRunAt == nil || !got.NextRunAt.Equal(next) {
		t.Fatalf("GetSchedule: %v %+v", err, got)
	}

	due, err := s.ListDueSchedules(time.Now())
	if err != nil || len(due) != 1 || due[0].ID != sc.ID {
		t.Fatalf("ListDueSchedules: %v, len=%d", err, len(due))
	}

	// Advance to a future occurrence: no longer due, history recorded.
	run, _ := s.CreateRun(d.ID, model.RunIncremental)
	future := next.Add(24 * time.Hour)
	if err := s.AdvanceSchedule(sc.ID, "fired", run.ID, time.Now(), &future); err != nil {
		t.Fatalf("AdvanceSchedule: %v", err)
	}
	if err := s.SetRunTrigger(run.ID, sc.ID); err != nil {
		t.Fatalf("SetRunTrigger: %v", err)
	}
	got, _ = s.GetSchedule(sc.ID)
	if got.LastResult != "fired" || got.LastRunID != run.ID || got.LastFiredAt == nil || !got.NextRunAt.Equal(future) {
		t.Fatalf("after advance: %+v", got)
	}
	due, _ = s.ListDueSchedules(time.Now())
	if len(due) != 0 {
		t.Fatalf("expected nothing due, got %d", len(due))
	}
	runs, _ := s.ListRuns(d.ID)
	if len(runs) != 1 || runs[0].TriggeredBy != sc.ID {
		t.Fatalf("run trigger not recorded: %+v", runs[0])
	}

	// A nil next disables the schedule (spent one-shot).
	if err := s.AdvanceSchedule(sc.ID, "fired", run.ID, time.Now(), nil); err != nil {
		t.Fatalf("AdvanceSchedule(nil): %v", err)
	}
	got, _ = s.GetSchedule(sc.ID)
	if got.Enabled || got.NextRunAt != nil {
		t.Fatalf("spent schedule not disabled: %+v", got)
	}

	// Teardown completion drops the deployment's schedules.
	if err := s.MarkDeploymentDeleted(d.ID); err != nil {
		t.Fatalf("MarkDeploymentDeleted: %v", err)
	}
	if _, err := s.GetSchedule(sc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("schedule should be gone after teardown, got %v", err)
	}
}
