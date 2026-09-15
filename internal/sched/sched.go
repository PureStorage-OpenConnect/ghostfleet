// Package sched fires schedules: per-deployment timers that kick off runs
// (incremental, verify, power on/off, teardown) through the same orchestrator
// entry points the API uses. A single tick loop polls the store for due
// schedules — next fire times are persisted, so schedules survive controller
// restarts, and a fire missed while the controller was down is caught up once
// (never replayed per occurrence).
package sched

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/orch"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/store"
)

// tickInterval is how often due schedules are polled. A 30s tick bounds fire
// latency well below any sensible cadence and shrugs off clock jumps/DST.
var tickInterval = 30 * time.Second

// busyGrace is how long a due schedule keeps retrying while its deployment is
// busy with another job before the occurrence is skipped.
var busyGrace = time.Hour

// Starter is the slice of the orchestrator the scheduler needs.
type Starter interface {
	StartFill(d *model.Deployment, runType string) (*model.Run, error)
	StartPower(d *model.Deployment, on bool) (*model.Run, error)
	StartTeardown(d *model.Deployment) (*model.Run, error)
}

// Store is the slice of the store the scheduler needs.
type Store interface {
	ListDueSchedules(t time.Time) ([]*model.Schedule, error)
	GetDeployment(id string) (*model.Deployment, error)
	AdvanceSchedule(id, result, runID string, firedAt time.Time, next *time.Time) error
	SetRunTrigger(runID, scheduleID string) error
}

// Scheduler drives all schedules from one goroutine.
type Scheduler struct {
	store   Store
	starter Starter
	now     func() time.Time

	// busySince tracks when a due schedule first hit a busy deployment, to
	// bound the retry window. In-memory only: a restart restarts the grace.
	busySince map[string]time.Time
}

// New wires a scheduler.
func New(st *store.Store, starter Starter) *Scheduler {
	return newScheduler(st, starter)
}

func newScheduler(st Store, starter Starter) *Scheduler {
	return &Scheduler{store: st, starter: starter, now: time.Now, busySince: make(map[string]time.Time)}
}

// Run ticks until the context is cancelled. Call in its own goroutine.
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	s.tick() // catch up fires missed while the controller was down
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick()
		}
	}
}

// tick fires every due schedule once.
func (s *Scheduler) tick() {
	now := s.now()
	due, err := s.store.ListDueSchedules(now)
	if err != nil {
		slog.Error("scheduler: listing due schedules", "err", err)
		return
	}
	for _, sc := range due {
		s.fire(sc, now)
	}
}

// fire attempts one schedule and records the outcome. A busy deployment keeps
// the schedule due (retried next tick) until busyGrace expires; every other
// outcome advances it to its next occurrence.
func (s *Scheduler) fire(sc *model.Schedule, now time.Time) {
	d, err := s.store.GetDeployment(sc.DeploymentID)
	if err != nil {
		// Deployment gone (or unreadable) — park the schedule instead of
		// erroring every tick forever.
		slog.Warn("scheduler: deployment missing, disabling schedule", "schedule", sc.ID, "err", err)
		s.advance(sc, now, "error: deployment missing", "", false)
		return
	}
	if d.Status == model.DeploymentDeleted || d.Status == model.DeploymentDeleting {
		s.advance(sc, now, "skipped: deployment deleted", "", false)
		return
	}

	run, err := s.start(sc, d)
	var busy orch.ErrBusy
	switch {
	case err == nil:
		if terr := s.store.SetRunTrigger(run.ID, sc.ID); terr != nil {
			slog.Warn("scheduler: stamping run trigger", "run", run.ID, "err", terr)
		}
		slog.Info("scheduler: fired", "schedule", sc.ID, "deployment", d.Name, "action", sc.Action, "run", run.ID)
		s.advance(sc, now, "fired", run.ID, true)
	case errors.As(err, &busy):
		since, seen := s.busySince[sc.ID]
		if !seen {
			s.busySince[sc.ID] = now
			return // stay due; retry next tick
		}
		if now.Sub(since) < busyGrace {
			return // still within the grace window
		}
		slog.Warn("scheduler: deployment busy past grace, skipping occurrence",
			"schedule", sc.ID, "deployment", d.Name, "action", sc.Action)
		s.advance(sc, now, "skipped: busy", "", true)
	default:
		// Preconditions (e.g. incremental before any fill) and other errors:
		// record and move on — retrying the same occurrence won't help.
		slog.Warn("scheduler: fire failed", "schedule", sc.ID, "deployment", d.Name, "action", sc.Action, "err", err)
		s.advance(sc, now, "error: "+err.Error(), "", true)
	}
}

// start maps the schedule's action onto the orchestrator, mirroring the API
// handlers' status guards.
func (s *Scheduler) start(sc *model.Schedule, d *model.Deployment) (*model.Run, error) {
	switch sc.Action {
	case model.RunIncremental, model.RunVerify:
		if d.Status != model.DeploymentReady {
			return nil, orch.ErrPrecondition{Reason: "deployment must be ready (deploy it first)"}
		}
		return s.starter.StartFill(d, sc.Action)
	case model.RunPowerOn:
		return s.starter.StartPower(d, true)
	case model.RunPowerOff:
		return s.starter.StartPower(d, false)
	case model.RunTeardown:
		// A teardown with nothing deployed still runs through the job path
		// and marks the deployment deleted — no special casing needed.
		return s.starter.StartTeardown(d)
	}
	return nil, fmt.Errorf("unknown action %q", sc.Action)
}

// advance records the outcome and moves the schedule to its next occurrence
// (or disables it: a spent one-shot, or recur=false).
func (s *Scheduler) advance(sc *model.Schedule, now time.Time, result, runID string, recur bool) {
	delete(s.busySince, sc.ID)
	var next *time.Time
	if recur {
		if n, ok := sc.NextAfter(now); ok {
			next = &n
		}
	}
	if err := s.store.AdvanceSchedule(sc.ID, result, runID, now, next); err != nil {
		slog.Error("scheduler: advancing schedule", "schedule", sc.ID, "err", err)
	}
}
