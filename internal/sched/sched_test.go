package sched

import (
	"strings"
	"testing"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/orch"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/store"
)

// fakeStore holds schedules and deployments in memory and records advances.
type fakeStore struct {
	schedules   map[string]*model.Schedule
	deployments map[string]*model.Deployment
	triggers    map[string]string // run ID -> schedule ID
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		schedules:   make(map[string]*model.Schedule),
		deployments: make(map[string]*model.Deployment),
		triggers:    make(map[string]string),
	}
}

func (f *fakeStore) ListDueSchedules(t time.Time) ([]*model.Schedule, error) {
	var due []*model.Schedule
	for _, sc := range f.schedules {
		if sc.Enabled && sc.NextRunAt != nil && !sc.NextRunAt.After(t) {
			due = append(due, sc)
		}
	}
	return due, nil
}

func (f *fakeStore) GetDeployment(id string) (*model.Deployment, error) {
	d, ok := f.deployments[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return d, nil
}

func (f *fakeStore) AdvanceSchedule(id, result, runID string, firedAt time.Time, next *time.Time) error {
	sc, ok := f.schedules[id]
	if !ok {
		return store.ErrNotFound
	}
	sc.LastResult, sc.LastRunID, sc.LastFiredAt = result, runID, &firedAt
	sc.NextRunAt, sc.Enabled = next, next != nil
	return nil
}

func (f *fakeStore) SetRunTrigger(runID, scheduleID string) error {
	f.triggers[runID] = scheduleID
	return nil
}

// fakeStarter records started runs and can simulate busy/precondition errors.
type fakeStarter struct {
	err     error
	started []string // "<deployment>/<runType>"
}

func (f *fakeStarter) start(d *model.Deployment, runType string) (*model.Run, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.started = append(f.started, d.ID+"/"+runType)
	return &model.Run{ID: "run-" + runType, DeploymentID: d.ID, Type: runType}, nil
}

func (f *fakeStarter) StartFill(d *model.Deployment, runType string) (*model.Run, error) {
	return f.start(d, runType)
}

func (f *fakeStarter) StartPower(d *model.Deployment, on bool) (*model.Run, error) {
	if on {
		return f.start(d, model.RunPowerOn)
	}
	return f.start(d, model.RunPowerOff)
}

func (f *fakeStarter) StartTeardown(d *model.Deployment) (*model.Run, error) {
	return f.start(d, model.RunTeardown)
}

func testSetup(t *testing.T) (*fakeStore, *fakeStarter, *Scheduler, *time.Time) {
	t.Helper()
	fs, st := newFakeStore(), &fakeStarter{}
	s := newScheduler(fs, st)
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	fs.deployments["d1"] = &model.Deployment{ID: "d1", Name: "dep", Status: model.DeploymentReady}
	return fs, st, s, &now
}

// due returns a schedule whose next fire time has arrived.
func due(now time.Time) *model.Schedule {
	past := now.Add(-time.Minute)
	return &model.Schedule{
		ID: "s1", DeploymentID: "d1", Action: model.RunIncremental,
		Kind: model.ScheduleEvery, Spec: "24h", Enabled: true, NextRunAt: &past,
	}
}

func TestFireAdvancesAndStampsRun(t *testing.T) {
	fs, st, s, now := testSetup(t)
	fs.schedules["s1"] = due(*now)

	s.tick()

	if len(st.started) != 1 || st.started[0] != "d1/incremental" {
		t.Fatalf("started = %v", st.started)
	}
	sc := fs.schedules["s1"]
	if sc.LastResult != "fired" || sc.LastRunID != "run-incremental" {
		t.Fatalf("after fire: %+v", sc)
	}
	if fs.triggers["run-incremental"] != "s1" {
		t.Fatalf("run not stamped: %v", fs.triggers)
	}
	want := now.Add(24 * time.Hour)
	if sc.NextRunAt == nil || !sc.NextRunAt.Equal(want) {
		t.Fatalf("next = %v, want %v", sc.NextRunAt, want)
	}

	// Not due anymore: another tick is a no-op.
	s.tick()
	if len(st.started) != 1 {
		t.Fatalf("re-fired although not due: %v", st.started)
	}
}

func TestOnceDisablesAfterFiring(t *testing.T) {
	fs, st, s, now := testSetup(t)
	past := now.Add(-time.Minute)
	fs.schedules["s1"] = &model.Schedule{
		ID: "s1", DeploymentID: "d1", Action: model.RunTeardown,
		Kind: model.ScheduleOnce, Spec: past.Format(time.RFC3339),
		Enabled: true, NextRunAt: &past,
	}

	s.tick()

	if len(st.started) != 1 || st.started[0] != "d1/teardown" {
		t.Fatalf("started = %v", st.started)
	}
	sc := fs.schedules["s1"]
	if sc.Enabled || sc.NextRunAt != nil {
		t.Fatalf("one-shot not disabled: %+v", sc)
	}
}

func TestBusyRetriesThenSkips(t *testing.T) {
	fs, st, s, now := testSetup(t)
	fs.schedules["s1"] = due(*now)
	st.err = orch.ErrBusy{DeploymentID: "d1"}

	// While busy and within grace the schedule stays due, untouched.
	s.tick()
	*now = now.Add(30 * time.Minute)
	s.tick()
	if sc := fs.schedules["s1"]; sc.LastResult != "" {
		t.Fatalf("advanced during grace: %+v", sc)
	}

	// Past the grace window the occurrence is skipped and the timer advances.
	*now = now.Add(31 * time.Minute)
	s.tick()
	sc := fs.schedules["s1"]
	if sc.LastResult != "skipped: busy" || sc.NextRunAt == nil {
		t.Fatalf("not skipped after grace: %+v", sc)
	}

	// Once the deployment frees up, the next occurrence fires normally.
	st.err = nil
	*now = now.Add(25 * time.Hour)
	s.tick()
	if len(st.started) != 1 || fs.schedules["s1"].LastResult != "fired" {
		t.Fatalf("did not recover after busy: %v %+v", st.started, fs.schedules["s1"])
	}
}

func TestPreconditionAdvancesWithError(t *testing.T) {
	fs, st, s, now := testSetup(t)
	fs.schedules["s1"] = due(*now)
	st.err = orch.ErrPrecondition{Reason: "run an initial fill before a incremental"}

	s.tick()

	sc := fs.schedules["s1"]
	if !strings.HasPrefix(sc.LastResult, "error:") || sc.NextRunAt == nil {
		t.Fatalf("precondition not recorded/advanced: %+v", sc)
	}
	if len(st.started) != 0 {
		t.Fatalf("unexpected start: %v", st.started)
	}
}

func TestDeletedDeploymentParksSchedule(t *testing.T) {
	fs, st, s, now := testSetup(t)
	fs.schedules["s1"] = due(*now)
	fs.deployments["d1"].Status = model.DeploymentDeleted

	s.tick()

	sc := fs.schedules["s1"]
	if sc.Enabled || sc.LastResult != "skipped: deployment deleted" {
		t.Fatalf("schedule not parked: %+v", sc)
	}
	if len(st.started) != 0 {
		t.Fatalf("unexpected start: %v", st.started)
	}
}

func TestMissingDeploymentParksSchedule(t *testing.T) {
	fs, st, s, now := testSetup(t)
	sc := due(*now)
	sc.DeploymentID = "gone"
	fs.schedules["s1"] = sc

	s.tick()

	if sc.Enabled || !strings.HasPrefix(sc.LastResult, "error:") {
		t.Fatalf("schedule not parked: %+v", sc)
	}
	if len(st.started) != 0 {
		t.Fatalf("unexpected start: %v", st.started)
	}
}
