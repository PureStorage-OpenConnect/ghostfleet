package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

type profileRequest struct {
	Name string             `json:"name,omitempty"`
	Spec *model.ProfileSpec `json:"spec,omitempty"`
}

// profileExportKind tags the portable profile document so an import can
// recognise it (PRO-4: profiles are the only state worth moving between
// controllers — full controller backup/restore is intentionally out of scope).
const profileExportKind = "ghostfleet.profile"

// profileDocument is the self-contained JSON for exporting/importing a profile:
// its name and the current version's spec. Versions/timestamps/ids are
// controller-local and deliberately omitted.
type profileDocument struct {
	Kind string             `json:"kind"`
	Name string             `json:"name"`
	Spec *model.ProfileSpec `json:"spec"`
}

func (s *server) createProfile(w http.ResponseWriter, r *http.Request) {
	var req profileRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name == "" || req.Spec == nil {
		writeError(w, http.StatusBadRequest, "name and spec are required")
		return
	}
	req.Spec.ApplyDefaults()
	if err := req.Spec.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := s.cfg.Store.CreateProfile(req.Name, *req.Spec)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// updateProfile renames and/or snapshots a new spec version (PRO-4).
func (s *server) updateProfile(w http.ResponseWriter, r *http.Request) {
	var req profileRequest
	if !readJSON(w, r, &req) {
		return
	}
	id := r.PathValue("id")
	if req.Name == "" && req.Spec == nil {
		writeError(w, http.StatusBadRequest, "nothing to update: provide name and/or spec")
		return
	}
	if req.Name != "" {
		if err := s.cfg.Store.RenameProfile(id, req.Name); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if req.Spec != nil {
		req.Spec.ApplyDefaults()
		if err := req.Spec.Validate(); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if _, err := s.cfg.Store.UpdateProfileSpec(id, *req.Spec); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	p, err := s.cfg.Store.GetProfile(id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// exportProfile returns the profile's current version as a portable JSON
// document, served as a downloadable attachment.
func (s *server) exportProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.cfg.Store.GetProfile(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	doc := profileDocument{Kind: profileExportKind, Name: p.Name, Spec: &p.Spec}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encoding profile")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", profileFilename(p.Name)))
	w.Write(body)
}

// importProfile creates a new profile from an exported document. The name is
// taken from the document (the UI lets the user adjust it first); a clashing
// name is a 409 so nothing is silently overwritten.
func (s *server) importProfile(w http.ResponseWriter, r *http.Request) {
	var doc profileDocument
	if !readJSON(w, r, &doc) {
		return
	}
	if doc.Kind != "" && doc.Kind != profileExportKind {
		writeError(w, http.StatusBadRequest, "not a GhostFleet profile document")
		return
	}
	if doc.Name == "" || doc.Spec == nil {
		writeError(w, http.StatusBadRequest, "name and spec are required")
		return
	}
	doc.Spec.ApplyDefaults()
	if err := doc.Spec.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p, err := s.cfg.Store.CreateProfile(doc.Name, *doc.Spec)
	if err != nil {
		writeStoreError(w, err) // 409 on a duplicate name
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// profileFilename builds a safe download filename from a profile name.
func profileFilename(name string) string {
	slug := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, name)
	if slug == "" {
		slug = "profile"
	}
	return slug + ".ghostfleet-profile.json"
}

func (s *server) getProfile(w http.ResponseWriter, r *http.Request) {
	p, err := s.cfg.Store.GetProfile(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) listProfiles(w http.ResponseWriter, _ *http.Request) {
	list, err := s.cfg.Store.ListProfiles()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if list == nil {
		list = []*model.Profile{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	if err := s.cfg.Store.DeleteProfile(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) listProfileVersions(w http.ResponseWriter, r *http.Request) {
	list, err := s.cfg.Store.ListProfileVersions(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if len(list) == 0 {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) getProfileVersion(w http.ResponseWriter, r *http.Request) {
	version, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "version must be an integer")
		return
	}
	v, err := s.cfg.Store.GetProfileVersion(r.PathValue("id"), version)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
