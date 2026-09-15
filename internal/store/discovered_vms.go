package store

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

const discoveredVMCols = `id, mac, token, status, inspection, first_seen_at, last_seen_at, agent_seen_at`

// EnsureDiscoveredVM returns the discovery record for a MAC, creating it (with
// a fresh discovery token) on first sight and bumping last_seen_at on every
// call — the netboot script endpoint calls this whenever an unknown MAC boots.
func (s *Store) EnsureDiscoveredVM(mac string) (*model.DiscoveredVM, error) {
	mac = strings.ToLower(mac)
	ts := now().Format(timeFmt)
	if _, err := s.db.Exec(`INSERT INTO discovered_vms (id, mac, token, status, first_seen_at, last_seen_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(mac) DO UPDATE SET last_seen_at = excluded.last_seen_at`,
		newID(), mac, newID(), model.DiscoveredNew, ts, ts); err != nil {
		return nil, err
	}
	row := s.db.QueryRow(`SELECT `+discoveredVMCols+` FROM discovered_vms WHERE mac = ?`, mac)
	return scanDiscoveredVM(row)
}

// GetDiscoveredVM returns one discovered VM by ID.
func (s *Store) GetDiscoveredVM(id string) (*model.DiscoveredVM, error) {
	row := s.db.QueryRow(`SELECT `+discoveredVMCols+` FROM discovered_vms WHERE id = ?`, id)
	return scanDiscoveredVM(row)
}

// GetDiscoveredVMByToken authenticates a discovery agent by its token.
func (s *Store) GetDiscoveredVMByToken(token string) (*model.DiscoveredVM, error) {
	row := s.db.QueryRow(`SELECT `+discoveredVMCols+` FROM discovered_vms WHERE token = ?`, token)
	return scanDiscoveredVM(row)
}

// ListDiscoveredVMs returns all discovered VMs, most recently seen first.
func (s *Store) ListDiscoveredVMs() ([]*model.DiscoveredVM, error) {
	rows, err := s.db.Query(`SELECT ` + discoveredVMCols + ` FROM discovered_vms ORDER BY last_seen_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.DiscoveredVM
	for rows.Next() {
		v, err := scanDiscoveredVM(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SetDiscoveredInspection stores the agent's disk-inspection report and marks
// the agent as seen.
func (s *Store) SetDiscoveredInspection(id, inspection string) error {
	ts := now().Format(timeFmt)
	res, err := s.db.Exec(`UPDATE discovered_vms
		SET inspection = ?, last_seen_at = ?, agent_seen_at = ? WHERE id = ?`,
		inspection, ts, ts, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchDiscoveredAgent refreshes a discovery agent's liveness (heartbeat).
func (s *Store) TouchDiscoveredAgent(id string) error {
	ts := now().Format(timeFmt)
	_, err := s.db.Exec(`UPDATE discovered_vms SET last_seen_at = ?, agent_seen_at = ? WHERE id = ?`,
		ts, ts, id)
	return err
}

// SetDiscoveredStatus transitions a discovered VM (new → adopted). An adopted
// VM's agent is told to reboot on its next heartbeat, after which its MAC
// resolves to a managed VM and the discovery record is only history.
func (s *Store) SetDiscoveredStatus(id, status string) error {
	res, err := s.db.Exec(`UPDATE discovered_vms SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteDiscoveredVM dismisses a discovery record. If the machine PXE-boots
// again it will simply be re-discovered.
func (s *Store) DeleteDiscoveredVM(id string) error {
	_, err := s.db.Exec(`DELETE FROM discovered_vms WHERE id = ?`, id)
	return err
}

func scanDiscoveredVM(r rowScanner) (*model.DiscoveredVM, error) {
	var v model.DiscoveredVM
	var first, last string
	var agentSeen sql.NullString
	err := r.Scan(&v.ID, &v.MAC, &v.Token, &v.Status, &v.Inspection, &first, &last, &agentSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v.FirstSeenAt, _ = time.Parse(timeFmt, first)
	v.LastSeenAt, _ = time.Parse(timeFmt, last)
	if agentSeen.Valid {
		t, _ := time.Parse(timeFmt, agentSeen.String)
		v.AgentSeenAt = &t
	}
	return &v, nil
}
