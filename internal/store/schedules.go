package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

// CreateSchedule stores a new schedule. The caller sets NextRunAt (computed
// from the spec) and Enabled.
func (s *Store) CreateSchedule(sc *model.Schedule) (*model.Schedule, error) {
	sc.ID, sc.CreatedAt, sc.UpdatedAt = newID(), now(), now()
	_, err := s.db.Exec(`INSERT INTO schedules
		(id, deployment_id, action, kind, spec, timezone, enabled, next_run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sc.ID, sc.DeploymentID, sc.Action, sc.Kind, sc.Spec, sc.Timezone,
		sc.Enabled, nullableTime(sc.NextRunAt), sc.CreatedAt.Format(timeFmt), sc.UpdatedAt.Format(timeFmt))
	if err != nil {
		return nil, err
	}
	return sc, nil
}

// UpdateSchedule replaces a schedule's definition (action, kind, spec,
// timezone, enabled) and its recomputed NextRunAt. The fire-history fields
// are left untouched.
func (s *Store) UpdateSchedule(sc *model.Schedule) error {
	res, err := s.db.Exec(`UPDATE schedules
		SET action = ?, kind = ?, spec = ?, timezone = ?, enabled = ?, next_run_at = ?, updated_at = ?
		WHERE id = ?`,
		sc.Action, sc.Kind, sc.Spec, sc.Timezone, sc.Enabled,
		nullableTime(sc.NextRunAt), now().Format(timeFmt), sc.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AdvanceSchedule records the outcome of a fire attempt and moves the
// schedule to its next occurrence. next == nil disables it (spent one-shot).
func (s *Store) AdvanceSchedule(id, result, runID string, firedAt time.Time, next *time.Time) error {
	res, err := s.db.Exec(`UPDATE schedules
		SET last_fired_at = ?, last_run_id = ?, last_result = ?, next_run_at = ?, enabled = ?, updated_at = ?
		WHERE id = ?`,
		firedAt.Format(timeFmt), nullable(runID), result,
		nullableTime(next), next != nil, now().Format(timeFmt), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSchedule removes a schedule.
func (s *Store) DeleteSchedule(id string) error {
	res, err := s.db.Exec(`DELETE FROM schedules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteDeploymentSchedules removes all schedules of a deployment — called
// when a teardown completes, since deployment rows are kept for history and
// the ON DELETE CASCADE therefore never applies.
func (s *Store) DeleteDeploymentSchedules(deploymentID string) error {
	_, err := s.db.Exec(`DELETE FROM schedules WHERE deployment_id = ?`, deploymentID)
	return err
}

// GetSchedule returns one schedule.
func (s *Store) GetSchedule(id string) (*model.Schedule, error) {
	row := s.db.QueryRow(scheduleSelect+` WHERE id = ?`, id)
	return scanSchedule(row)
}

// ListSchedules returns a deployment's schedules, oldest first.
func (s *Store) ListSchedules(deploymentID string) ([]*model.Schedule, error) {
	return s.querySchedules(scheduleSelect+` WHERE deployment_id = ? ORDER BY created_at`, deploymentID)
}

// ListDueSchedules returns every enabled schedule whose next fire time has
// arrived — the scheduler's per-tick work list.
func (s *Store) ListDueSchedules(t time.Time) ([]*model.Schedule, error) {
	return s.querySchedules(scheduleSelect+` WHERE enabled AND next_run_at IS NOT NULL AND next_run_at <= ?
		ORDER BY next_run_at`, t.UTC().Format(timeFmt))
}

// SetRunTrigger stamps a run with the schedule that fired it.
func (s *Store) SetRunTrigger(runID, scheduleID string) error {
	res, err := s.db.Exec(`UPDATE runs SET triggered_by = ? WHERE id = ?`, scheduleID, runID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

const scheduleSelect = `SELECT id, deployment_id, action, kind, spec, timezone, enabled,
	next_run_at, last_fired_at, last_run_id, last_result, created_at, updated_at FROM schedules`

func (s *Store) querySchedules(query string, args ...any) ([]*model.Schedule, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*model.Schedule
	for rows.Next() {
		sc, err := scanSchedule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

func scanSchedule(r rowScanner) (*model.Schedule, error) {
	var sc model.Schedule
	var next, fired, runID, result sql.NullString
	var created, updated string
	err := r.Scan(&sc.ID, &sc.DeploymentID, &sc.Action, &sc.Kind, &sc.Spec, &sc.Timezone,
		&sc.Enabled, &next, &fired, &runID, &result, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if next.Valid {
		t, _ := time.Parse(timeFmt, next.String)
		sc.NextRunAt = &t
	}
	if fired.Valid {
		t, _ := time.Parse(timeFmt, fired.String)
		sc.LastFiredAt = &t
	}
	sc.LastRunID = runID.String
	sc.LastResult = result.String
	sc.CreatedAt, _ = time.Parse(timeFmt, created)
	sc.UpdatedAt, _ = time.Parse(timeFmt, updated)
	return &sc, nil
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(timeFmt)
}
