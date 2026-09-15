package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

// CreateProfile stores a new profile with its spec as version 1.
func (s *Store) CreateProfile(name string, spec model.ProfileSpec) (*model.Profile, error) {
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	id, ts := newID(), now()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	_, err = tx.Exec(`INSERT INTO profiles (id, name, current_version, created_at, updated_at)
		VALUES (?, ?, 1, ?, ?)`, id, name, ts.Format(timeFmt), ts.Format(timeFmt))
	if isUniqueViolation(err) {
		return nil, ErrConflict{Reason: fmt.Sprintf("profile name %q already exists", name)}
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`INSERT INTO profile_versions (profile_id, version, spec, created_at)
		VALUES (?, 1, ?, ?)`, id, string(specJSON), ts.Format(timeFmt)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &model.Profile{ID: id, Name: name, CurrentVersion: 1, Spec: spec, CreatedAt: ts, UpdatedAt: ts}, nil
}

// UpdateProfileSpec snapshots the new spec as the next version (PRO-4).
func (s *Store) UpdateProfileSpec(id string, spec model.ProfileSpec) (*model.Profile, error) {
	specJSON, err := json.Marshal(spec)
	if err != nil {
		return nil, err
	}
	ts := now()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	var current int
	err = tx.QueryRow(`SELECT current_version FROM profiles WHERE id = ?`, id).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	next := current + 1
	if _, err := tx.Exec(`INSERT INTO profile_versions (profile_id, version, spec, created_at)
		VALUES (?, ?, ?, ?)`, id, next, string(specJSON), ts.Format(timeFmt)); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE profiles SET current_version = ?, updated_at = ? WHERE id = ?`,
		next, ts.Format(timeFmt), id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetProfile(id)
}

// RenameProfile changes the name without creating a new version.
func (s *Store) RenameProfile(id, name string) error {
	res, err := s.db.Exec(`UPDATE profiles SET name = ?, updated_at = ? WHERE id = ?`,
		name, now().Format(timeFmt), id)
	if isUniqueViolation(err) {
		return ErrConflict{Reason: fmt.Sprintf("profile name %q already exists", name)}
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

// GetProfile returns a profile with the spec of its current version.
func (s *Store) GetProfile(id string) (*model.Profile, error) {
	row := s.db.QueryRow(`
		SELECT p.id, p.name, p.current_version, v.spec, p.created_at, p.updated_at
		FROM profiles p
		JOIN profile_versions v ON v.profile_id = p.id AND v.version = p.current_version
		WHERE p.id = ?`, id)
	return scanProfile(row)
}

// ListProfiles returns all profiles with their current specs, newest first.
func (s *Store) ListProfiles() ([]*model.Profile, error) {
	rows, err := s.db.Query(`
		SELECT p.id, p.name, p.current_version, v.spec, p.created_at, p.updated_at
		FROM profiles p
		JOIN profile_versions v ON v.profile_id = p.id AND v.version = p.current_version
		ORDER BY p.created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*model.Profile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteProfile removes a profile and its versions. Profiles referenced by
// non-deleted deployments cannot be removed.
func (s *Store) DeleteProfile(id string) error {
	var inUse int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM deployments WHERE profile_id = ? AND status != ?`,
		id, model.DeploymentDeleted).Scan(&inUse); err != nil {
		return err
	}
	if inUse > 0 {
		return ErrConflict{Reason: fmt.Sprintf("profile is referenced by %d active deployment(s)", inUse)}
	}
	res, err := s.db.Exec(`DELETE FROM profiles WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// GetProfileVersion returns one immutable version snapshot.
func (s *Store) GetProfileVersion(id string, version int) (*model.ProfileVersion, error) {
	var v model.ProfileVersion
	var specJSON, created string
	err := s.db.QueryRow(`SELECT profile_id, version, spec, created_at
		FROM profile_versions WHERE profile_id = ? AND version = ?`, id, version).
		Scan(&v.ProfileID, &v.Version, &specJSON, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(specJSON), &v.Spec); err != nil {
		return nil, err
	}
	v.CreatedAt, _ = time.Parse(timeFmt, created)
	return &v, nil
}

// ListProfileVersions returns all versions of a profile, newest first.
func (s *Store) ListProfileVersions(id string) ([]*model.ProfileVersion, error) {
	rows, err := s.db.Query(`SELECT profile_id, version, spec, created_at
		FROM profile_versions WHERE profile_id = ? ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*model.ProfileVersion
	for rows.Next() {
		var v model.ProfileVersion
		var specJSON, created string
		if err := rows.Scan(&v.ProfileID, &v.Version, &specJSON, &created); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(specJSON), &v.Spec); err != nil {
			return nil, err
		}
		v.CreatedAt, _ = time.Parse(timeFmt, created)
		out = append(out, &v)
	}
	return out, rows.Err()
}

type rowScanner interface{ Scan(...any) error }

func scanProfile(r rowScanner) (*model.Profile, error) {
	var p model.Profile
	var specJSON, created, updated string
	err := r.Scan(&p.ID, &p.Name, &p.CurrentVersion, &specJSON, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(specJSON), &p.Spec); err != nil {
		return nil, err
	}
	p.CreatedAt, _ = time.Parse(timeFmt, created)
	p.UpdatedAt, _ = time.Parse(timeFmt, updated)
	return &p, nil
}
