package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

const managedVMCols = `id, deployment_id, name, ref, disk_count, disk_size_gib,
	mac, boot_token, agent_status, agent_seen_at,
	data_state, data_run_id, data_manifest, data_seen_at,
	fill_run_id, fill_status, bytes_written, bytes_total, mbps, fill_error, created_at`

// AddManagedVM records a VM the orchestrator created on the hypervisor. A
// boot token is generated; the MAC is stored lower-case.
func (s *Store) AddManagedVM(v *model.ManagedVM) error {
	v.ID, v.CreatedAt = newID(), now()
	v.BootToken = newID()
	v.MAC = strings.ToLower(v.MAC)
	if v.AgentStatus == "" {
		v.AgentStatus = model.AgentNone
	}
	_, err := s.db.Exec(`INSERT INTO managed_vms
		(id, deployment_id, name, ref, disk_count, disk_size_gib, mac, boot_token, agent_status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ID, v.DeploymentID, v.Name, v.Ref, v.DiskCount, v.DiskSizeGiB,
		v.MAC, v.BootToken, v.AgentStatus, v.CreatedAt.Format(timeFmt))
	return err
}

// UpdateManagedVMDisks records the new disk shape after add/extend.
func (s *Store) UpdateManagedVMDisks(id string, diskCount, diskSizeGiB int) error {
	res, err := s.db.Exec(`UPDATE managed_vms SET disk_count = ?, disk_size_gib = ? WHERE id = ?`,
		diskCount, diskSizeGiB, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchAgent marks the VM's agent as online now (register and heartbeat).
func (s *Store) TouchAgent(vmID string) error {
	res, err := s.db.Exec(`UPDATE managed_vms SET agent_status = ?, agent_seen_at = ? WHERE id = ?`,
		model.AgentOnline, now().Format(timeFmt), vmID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResetAgent marks the VM's agent as gone (never registered), e.g. after the
// VM was powered off: a stale-but-recent heartbeat must not read as alive.
func (s *Store) ResetAgent(vmID string) error {
	res, err := s.db.Exec(`UPDATE managed_vms SET agent_status = ?, agent_seen_at = NULL WHERE id = ?`,
		model.AgentNone, vmID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetManagedVM returns one managed VM by ID.
func (s *Store) GetManagedVM(id string) (*model.ManagedVM, error) {
	row := s.db.QueryRow(`SELECT `+managedVMCols+` FROM managed_vms WHERE id = ?`, id)
	return scanManagedVM(row)
}

// GetManagedVMByToken authenticates an agent by its boot token.
func (s *Store) GetManagedVMByToken(token string) (*model.ManagedVM, error) {
	row := s.db.QueryRow(`SELECT `+managedVMCols+` FROM managed_vms WHERE boot_token = ?`, token)
	return scanManagedVM(row)
}

// GetManagedVMByMAC resolves the netboot script request for a VM.
func (s *Store) GetManagedVMByMAC(mac string) (*model.ManagedVM, error) {
	row := s.db.QueryRow(`SELECT `+managedVMCols+` FROM managed_vms WHERE mac = ?`, strings.ToLower(mac))
	return scanManagedVM(row)
}

// GetManagedVMByName returns a deployment's VM by its (hypervisor) name.
func (s *Store) GetManagedVMByName(deploymentID, name string) (*model.ManagedVM, error) {
	row := s.db.QueryRow(`SELECT `+managedVMCols+` FROM managed_vms
		WHERE deployment_id = ? AND name = ?`, deploymentID, name)
	return scanManagedVM(row)
}

// SetVMDataState records what the VM's disks hold (model.Data*), as reported
// by its agent or taken from a discovery report at adoption.
func (s *Store) SetVMDataState(vmID, state, runID string, manifest bool) error {
	res, err := s.db.Exec(`UPDATE managed_vms
		SET data_state = ?, data_run_id = ?, data_manifest = ?, data_seen_at = ? WHERE id = ?`,
		state, runID, manifest, now().Format(timeFmt), vmID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RebindManagedVM re-points an existing VM record at a different hypervisor
// VM — restore-in-place adoption: the restored copy takes the original's
// place in its deployment, under its new MAC and driver reference. Agent
// liveness and the on-disk data state are reset (the restored VM has never
// registered as this record; adoption re-sets the data state from its
// discovery report).
func (s *Store) RebindManagedVM(id, mac, ref string) error {
	res, err := s.db.Exec(`UPDATE managed_vms
		SET mac = ?, ref = ?, agent_status = ?, agent_seen_at = NULL,
		    data_state = '', data_run_id = '', data_manifest = 0, data_seen_at = NULL
		WHERE id = ?`,
		strings.ToLower(mac), ref, model.AgentNone, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteManagedVM removes the record after the VM is destroyed.
func (s *Store) DeleteManagedVM(id string) error {
	_, err := s.db.Exec(`DELETE FROM managed_vms WHERE id = ?`, id)
	return err
}

// ListManagedVMs returns a deployment's VMs ordered by name.
func (s *Store) ListManagedVMs(deploymentID string) ([]*model.ManagedVM, error) {
	rows, err := s.db.Query(`SELECT `+managedVMCols+` FROM managed_vms
		WHERE deployment_id = ? ORDER BY name`, deploymentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*model.ManagedVM
	for rows.Next() {
		v, err := scanManagedVM(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func scanManagedVM(r rowScanner) (*model.ManagedVM, error) {
	var v model.ManagedVM
	var seen, dataSeen sql.NullString
	var created string
	err := r.Scan(&v.ID, &v.DeploymentID, &v.Name, &v.Ref, &v.DiskCount, &v.DiskSizeGiB,
		&v.MAC, &v.BootToken, &v.AgentStatus, &seen,
		&v.DataState, &v.DataRunID, &v.DataManifest, &dataSeen,
		&v.FillRunID, &v.FillStatus, &v.BytesWritten, &v.BytesTotal, &v.MBps, &v.FillError, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if seen.Valid {
		t, _ := time.Parse(timeFmt, seen.String)
		v.AgentSeenAt = &t
	}
	if dataSeen.Valid {
		t, _ := time.Parse(timeFmt, dataSeen.String)
		v.DataSeenAt = &t
	}
	v.CreatedAt, _ = time.Parse(timeFmt, created)
	return &v, nil
}

// AssignFill marks a VM as pending for a fill/incremental run with its total
// byte target, resetting prior progress.
func (s *Store) AssignFill(vmID, runID string, bytesTotal int64) error {
	_, err := s.db.Exec(`UPDATE managed_vms
		SET fill_run_id = ?, fill_status = ?, bytes_written = 0, bytes_total = ?,
		    mbps = 0, fill_error = '', fill_updated_at = ?
		WHERE id = ?`,
		runID, model.FillPending, bytesTotal, now().Format(timeFmt), vmID)
	return err
}

// SetFillTotal corrects a VM's byte target from the agent's own plan. The
// controller's target is an estimate for an incremental (change%+growth% of
// the profile's data); the agent, once it has listed the existing files, knows
// exactly how much it will write.
func (s *Store) SetFillTotal(vmID string, bytesTotal int64) error {
	_, err := s.db.Exec(`UPDATE managed_vms SET bytes_total = ?
		WHERE id = ? AND bytes_total <> ?`, bytesTotal, vmID, bytesTotal)
	return err
}

// CountFillStatus counts VMs in a given fill status for a run.
func (s *Store) CountFillStatus(runID, status string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM managed_vms WHERE fill_run_id = ? AND fill_status = ?`,
		runID, status).Scan(&n)
	return n, err
}

// SetFillStatus transitions a VM's fill status (e.g. pending→working).
func (s *Store) SetFillStatus(vmID, status string) error {
	_, err := s.db.Exec(`UPDATE managed_vms SET fill_status = ?, fill_updated_at = ? WHERE id = ?`,
		status, now().Format(timeFmt), vmID)
	return err
}

// FailFillVM marks one VM's fill as failed with a reason — used by the boot
// watchdog when a VM exhausts its boot attempts without ever registering.
// Unlike ReportFillProgress it does not touch agent liveness: the agent was
// never seen, and marking it online would hide exactly that.
func (s *Store) FailFillVM(vmID, msg string) error {
	_, err := s.db.Exec(`UPDATE managed_vms SET fill_status = ?, fill_error = ?, fill_updated_at = ? WHERE id = ?`,
		model.FillFailed, msg, now().Format(timeFmt), vmID)
	return err
}

// CancelUnfinishedFills marks a run's still-pending/working VMs as failed with
// a "cancelled" note, so their agents go idle (the run is no longer running)
// and the UI stops showing them in flight.
func (s *Store) CancelUnfinishedFills(runID string) error {
	_, err := s.db.Exec(`UPDATE managed_vms
		SET fill_status = ?, fill_error = 'cancelled', fill_updated_at = ?
		WHERE fill_run_id = ? AND fill_status IN (?, ?)`,
		model.FillFailed, now().Format(timeFmt), runID, model.FillPending, model.FillWorking)
	return err
}

// ReportFillProgress records a progress sample from an agent. done/failed
// set the terminal status; otherwise the VM is marked working. A completed
// fill or incremental also settles the VM's on-disk data state: the agent
// has just written the data (and its markers and manifest), so the disks
// are known filled without waiting for the next boot-time inspection.
func (s *Store) ReportFillProgress(vmID string, bytesWritten int64, mbps float64, done bool, errMsg string) error {
	status := model.FillWorking
	if errMsg != "" {
		status = model.FillFailed
	} else if done {
		status = model.FillDone
	}
	ts := now().Format(timeFmt)
	_, err := s.db.Exec(`UPDATE managed_vms
		SET bytes_written = ?, mbps = ?, fill_status = ?, fill_error = ?,
		    agent_status = ?, agent_seen_at = ?, fill_updated_at = ?
		WHERE id = ?`,
		bytesWritten, mbps, status, errMsg,
		model.AgentOnline, ts, ts, vmID)
	if err != nil || status != model.FillDone {
		return err
	}
	_, err = s.db.Exec(`UPDATE managed_vms
		SET data_state = ?, data_run_id = fill_run_id, data_manifest = 1, data_seen_at = ?
		WHERE id = ? AND fill_run_id IN (SELECT id FROM runs WHERE type IN (?, ?))`,
		model.DataFilled, ts, vmID, model.RunInitialFill, model.RunIncremental)
	return err
}

// StartRun marks a run as running. started_at is preserved if already set, so
// resuming an interrupted run (NFR-3) keeps its original start time.
func (s *Store) StartRun(id string) error {
	res, err := s.db.Exec(`UPDATE runs SET status = ?, started_at = COALESCE(started_at, ?) WHERE id = ?`,
		model.RunRunning, now().Format(timeFmt), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRunStats updates a running run's stats JSON (live progress snapshots).
func (s *Store) SetRunStats(id, stats string) error {
	_, err := s.db.Exec(`UPDATE runs SET stats = ? WHERE id = ?`, stats, id)
	return err
}

// AddRunSample appends a throughput sample to a run's time series.
func (s *Store) AddRunSample(runID string, mbps float64, bytesWritten int64) error {
	_, err := s.db.Exec(`INSERT INTO run_samples (run_id, ts, mbps, bytes_written) VALUES (?, ?, ?, ?)`,
		runID, now().Format(timeFmt), mbps, bytesWritten)
	return err
}

// RunSample is one throughput data point.
type RunSample struct {
	TS           time.Time `json:"ts"`
	MBps         float64   `json:"mbps"`
	BytesWritten int64     `json:"bytesWritten"`
}

// ListRunSamples returns a run's throughput time series, oldest first.
func (s *Store) ListRunSamples(runID string) ([]RunSample, error) {
	rows, err := s.db.Query(`SELECT ts, mbps, bytes_written FROM run_samples
		WHERE run_id = ? ORDER BY ts`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunSample
	for rows.Next() {
		var smp RunSample
		var ts string
		if err := rows.Scan(&ts, &smp.MBps, &smp.BytesWritten); err != nil {
			return nil, err
		}
		smp.TS, _ = time.Parse(timeFmt, ts)
		out = append(out, smp)
	}
	return out, rows.Err()
}

// FinishRun records the final status and a JSON stats summary.
func (s *Store) FinishRun(id, status, stats string) error {
	res, err := s.db.Exec(`UPDATE runs SET status = ?, finished_at = ?, stats = ? WHERE id = ?`,
		status, now().Format(timeFmt), stats, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
