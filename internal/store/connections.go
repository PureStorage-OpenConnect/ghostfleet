package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

// CreateConnection stores a hypervisor connection; secretEnc is the
// already-encrypted credential blob.
func (s *Store) CreateConnection(c *model.Connection, secretEnc []byte) (*model.Connection, error) {
	c.ID, c.CreatedAt, c.UpdatedAt = newID(), now(), now()
	_, err := s.db.Exec(`INSERT INTO connections
		(id, name, plugin, endpoint, username, secret_enc, insecure_tls, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.Name, c.Plugin, c.Endpoint, c.Username, secretEnc, c.InsecureTLS,
		c.CreatedAt.Format(timeFmt), c.UpdatedAt.Format(timeFmt))
	if isUniqueViolation(err) {
		return nil, ErrConflict{Reason: fmt.Sprintf("connection name %q already exists", c.Name)}
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// UpdateConnection updates metadata; secretEnc is replaced only when non-nil.
func (s *Store) UpdateConnection(c *model.Connection, secretEnc []byte) error {
	var (
		res sql.Result
		err error
	)
	if secretEnc != nil {
		res, err = s.db.Exec(`UPDATE connections SET name=?, plugin=?, endpoint=?, username=?,
			secret_enc=?, insecure_tls=?, updated_at=? WHERE id=?`,
			c.Name, c.Plugin, c.Endpoint, c.Username, secretEnc, c.InsecureTLS,
			now().Format(timeFmt), c.ID)
	} else {
		res, err = s.db.Exec(`UPDATE connections SET name=?, plugin=?, endpoint=?, username=?,
			insecure_tls=?, updated_at=? WHERE id=?`,
			c.Name, c.Plugin, c.Endpoint, c.Username, c.InsecureTLS,
			now().Format(timeFmt), c.ID)
	}
	if isUniqueViolation(err) {
		return ErrConflict{Reason: fmt.Sprintf("connection name %q already exists", c.Name)}
	}
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetConnection returns a connection without its secret.
func (s *Store) GetConnection(id string) (*model.Connection, error) {
	row := s.db.QueryRow(`SELECT id, name, plugin, endpoint, username, insecure_tls,
		created_at, updated_at FROM connections WHERE id = ?`, id)
	return scanConnection(row)
}

// GetConnectionSecret returns the encrypted credential blob.
func (s *Store) GetConnectionSecret(id string) ([]byte, error) {
	var blob []byte
	err := s.db.QueryRow(`SELECT secret_enc FROM connections WHERE id = ?`, id).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return blob, err
}

// ListConnections returns all connections without secrets, newest first.
func (s *Store) ListConnections() ([]*model.Connection, error) {
	rows, err := s.db.Query(`SELECT id, name, plugin, endpoint, username, insecure_tls,
		created_at, updated_at FROM connections ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*model.Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteConnection removes a connection unless non-deleted deployments use it.
func (s *Store) DeleteConnection(id string) error {
	var inUse int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM deployments WHERE connection_id = ? AND status != ?`,
		id, model.DeploymentDeleted).Scan(&inUse); err != nil {
		return err
	}
	if inUse > 0 {
		return ErrConflict{Reason: fmt.Sprintf("connection is used by %d active deployment(s)", inUse)}
	}
	res, err := s.db.Exec(`DELETE FROM connections WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanConnection(r rowScanner) (*model.Connection, error) {
	var c model.Connection
	var created, updated string
	err := r.Scan(&c.ID, &c.Name, &c.Plugin, &c.Endpoint, &c.Username, &c.InsecureTLS, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.CreatedAt, _ = time.Parse(timeFmt, created)
	c.UpdatedAt, _ = time.Parse(timeFmt, updated)
	return &c, nil
}
