package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

// CreateDeployment stores a new deployment with its effective config (PRO-8).
func (s *Store) CreateDeployment(d *model.Deployment) (*model.Deployment, error) {
	specJSON, err := json.Marshal(d.Spec)
	if err != nil {
		return nil, err
	}
	placementJSON, err := json.Marshal(d.Placement)
	if err != nil {
		return nil, err
	}
	d.ID, d.CreatedAt, d.UpdatedAt = newID(), now(), now()
	if d.Status == "" {
		d.Status = model.DeploymentNew // adoption pre-sets ready (VMs already exist)
	}

	_, err = s.db.Exec(`INSERT INTO deployments
		(id, name, profile_id, profile_version, connection_id, spec, placement, status, origin, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID, d.Name, nullable(d.ProfileID), nullableInt(d.ProfileVersion), d.ConnectionID,
		string(specJSON), string(placementJSON), d.Status, d.Origin,
		d.CreatedAt.Format(timeFmt), d.UpdatedAt.Format(timeFmt))
	if isUniqueViolation(err) {
		return nil, ErrConflict{Reason: fmt.Sprintf("deployment name %q already exists", d.Name)}
	}
	if err != nil {
		return nil, err
	}
	return d, nil
}

// UpdateDeploymentSpec replaces the effective config (scale-up, PRO-6).
func (s *Store) UpdateDeploymentSpec(id string, spec model.ProfileSpec) error {
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(`UPDATE deployments SET spec = ?, updated_at = ? WHERE id = ?`,
		string(specJSON), now().Format(timeFmt), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDeploymentStatus transitions the lifecycle state.
func (s *Store) SetDeploymentStatus(id, status string) error {
	res, err := s.db.Exec(`UPDATE deployments SET status = ?, updated_at = ? WHERE id = ?`,
		status, now().Format(timeFmt), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkDeploymentDeleted marks a deployment deleted and frees its name for
// reuse. Torn-down deployments are kept for run history (PRO-7), so their
// UNIQUE name would otherwise stay reserved forever; the original name is
// preserved with a "(deleted <id8>)" suffix that stays unique. The status
// guard makes a retried teardown idempotent (no double suffix).
func (s *Store) MarkDeploymentDeleted(id string) error {
	res, err := s.db.Exec(`UPDATE deployments
		SET status = ?, name = name || ' (deleted ' || substr(id, 1, 8) || ')', updated_at = ?
		WHERE id = ? AND status != ?`,
		model.DeploymentDeleted, now().Format(timeFmt), id, model.DeploymentDeleted)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound // missing, or already deleted
	}
	// Deployment rows are kept for history, so the schedules' ON DELETE
	// CASCADE never applies — drop them here or timers would keep firing
	// against a deleted deployment.
	return s.DeleteDeploymentSchedules(id)
}

// GetDeployment returns one deployment.
func (s *Store) GetDeployment(id string) (*model.Deployment, error) {
	row := s.db.QueryRow(`SELECT id, name, profile_id, profile_version, connection_id,
		spec, placement, status, origin, created_at, updated_at FROM deployments WHERE id = ?`, id)
	return scanDeployment(row)
}

// ListDeployments returns all deployments, newest first.
func (s *Store) ListDeployments() ([]*model.Deployment, error) {
	rows, err := s.db.Query(`SELECT id, name, profile_id, profile_version, connection_id,
		spec, placement, status, origin, created_at, updated_at FROM deployments ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*model.Deployment
	for rows.Next() {
		d, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// CreateRun records a new run for a deployment.
func (s *Store) CreateRun(deploymentID, runType string) (*model.Run, error) {
	r := &model.Run{
		ID:           newID(),
		DeploymentID: deploymentID,
		Type:         runType,
		Status:       model.RunPending,
		CreatedAt:    now(),
	}
	_, err := s.db.Exec(`INSERT INTO runs (id, deployment_id, type, status, created_at)
		VALUES (?, ?, ?, ?, ?)`, r.ID, r.DeploymentID, r.Type, r.Status, r.CreatedAt.Format(timeFmt))
	if err != nil {
		return nil, err
	}
	return r, nil
}

// HasSucceededRun reports whether the deployment has a succeeded run of the
// given type — used to require an initial fill before an incremental.
func (s *Store) HasSucceededRun(deploymentID, runType string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM runs WHERE deployment_id = ? AND type = ? AND status = ?`,
		deploymentID, runType, model.RunSucceeded).Scan(&n)
	return n > 0, err
}

// FilledDeploymentIDs returns the set of deployment IDs with at least one
// succeeded initial-fill run — one query to gate the incremental action across
// the whole deployment list.
func (s *Store) FilledDeploymentIDs() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT DISTINCT deployment_id FROM runs WHERE type = ? AND status = ?`,
		model.RunInitialFill, model.RunSucceeded)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// GetRun returns one run by ID.
func (s *Store) GetRun(id string) (*model.Run, error) {
	var r model.Run
	var started, finished, stats, trigger sql.NullString
	var created string
	err := s.db.QueryRow(`SELECT id, deployment_id, type, status, started_at, finished_at, stats, triggered_by, created_at
		FROM runs WHERE id = ?`, id).
		Scan(&r.ID, &r.DeploymentID, &r.Type, &r.Status, &started, &finished, &stats, &trigger, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if started.Valid {
		t, _ := time.Parse(timeFmt, started.String)
		r.StartedAt = &t
	}
	if finished.Valid {
		t, _ := time.Parse(timeFmt, finished.String)
		r.FinishedAt = &t
	}
	r.Stats = stats.String
	r.TriggeredBy = trigger.String
	r.CreatedAt, _ = time.Parse(timeFmt, created)
	return &r, nil
}

// ListRuns returns a deployment's runs, newest first.
func (s *Store) ListRuns(deploymentID string) ([]*model.Run, error) {
	return s.queryRuns(`SELECT id, deployment_id, type, status, started_at, finished_at, stats, triggered_by, created_at
		FROM runs WHERE deployment_id = ? ORDER BY created_at DESC`, deploymentID)
}

// ListRunningRuns returns every run still marked running — used at startup to
// resume jobs orphaned by a controller restart (NFR-2/NFR-3).
func (s *Store) ListRunningRuns() ([]*model.Run, error) {
	return s.queryRuns(`SELECT id, deployment_id, type, status, started_at, finished_at, stats, triggered_by, created_at
		FROM runs WHERE status = ? ORDER BY created_at`, model.RunRunning)
}

func (s *Store) queryRuns(query string, args ...any) ([]*model.Run, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*model.Run
	for rows.Next() {
		var r model.Run
		var started, finished, stats, trigger sql.NullString
		var created string
		if err := rows.Scan(&r.ID, &r.DeploymentID, &r.Type, &r.Status, &started, &finished, &stats, &trigger, &created); err != nil {
			return nil, err
		}
		if started.Valid {
			t, _ := time.Parse(timeFmt, started.String)
			r.StartedAt = &t
		}
		if finished.Valid {
			t, _ := time.Parse(timeFmt, finished.String)
			r.FinishedAt = &t
		}
		r.Stats = stats.String
		r.TriggeredBy = trigger.String
		r.CreatedAt, _ = time.Parse(timeFmt, created)
		out = append(out, &r)
	}
	return out, rows.Err()
}

func scanDeployment(r rowScanner) (*model.Deployment, error) {
	var d model.Deployment
	var profileID sql.NullString
	var profileVersion sql.NullInt64
	var specJSON, placementJSON, created, updated string
	err := r.Scan(&d.ID, &d.Name, &profileID, &profileVersion, &d.ConnectionID,
		&specJSON, &placementJSON, &d.Status, &d.Origin, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.ProfileID = profileID.String
	d.ProfileVersion = int(profileVersion.Int64)
	if err := json.Unmarshal([]byte(specJSON), &d.Spec); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(placementJSON), &d.Placement); err != nil {
		return nil, err
	}
	d.CreatedAt, _ = time.Parse(timeFmt, created)
	d.UpdatedAt, _ = time.Parse(timeFmt, updated)
	return &d, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(i int) any {
	if i == 0 {
		return nil
	}
	return i
}
