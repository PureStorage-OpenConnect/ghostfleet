package api

import (
	"net/http"
	"slices"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

// connectionRequest is the write payload; Secret is write-only (NFR-6).
type connectionRequest struct {
	Name        string `json:"name"`
	Plugin      string `json:"plugin"`
	Endpoint    string `json:"endpoint"`
	Username    string `json:"username"`
	Secret      string `json:"secret,omitempty"`
	InsecureTLS bool   `json:"insecureTLS,omitempty"`
}

func (cr *connectionRequest) validate(secretRequired bool) string {
	switch {
	case cr.Name == "":
		return "name is required"
	case !slices.Contains(model.KnownPlugins, cr.Plugin):
		return "unknown plugin (supported: vsphere)"
	case cr.Endpoint == "":
		return "endpoint is required"
	case cr.Username == "":
		return "username is required"
	case secretRequired && cr.Secret == "":
		return "secret is required"
	}
	return ""
}

func (s *server) createConnection(w http.ResponseWriter, r *http.Request) {
	var req connectionRequest
	if !readJSON(w, r, &req) {
		return
	}
	if msg := req.validate(true); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	blob, err := s.cfg.Secrets.Encrypt(req.Secret)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	c, err := s.cfg.Store.CreateConnection(&model.Connection{
		Name: req.Name, Plugin: req.Plugin, Endpoint: req.Endpoint,
		Username: req.Username, InsecureTLS: req.InsecureTLS,
	}, blob)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *server) updateConnection(w http.ResponseWriter, r *http.Request) {
	var req connectionRequest
	if !readJSON(w, r, &req) {
		return
	}
	if msg := req.validate(false); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	var blob []byte // nil keeps the stored secret
	if req.Secret != "" {
		var err error
		if blob, err = s.cfg.Secrets.Encrypt(req.Secret); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	c := &model.Connection{
		ID: r.PathValue("id"), Name: req.Name, Plugin: req.Plugin,
		Endpoint: req.Endpoint, Username: req.Username, InsecureTLS: req.InsecureTLS,
	}
	if err := s.cfg.Store.UpdateConnection(c, blob); err != nil {
		writeStoreError(w, err)
		return
	}
	updated, err := s.cfg.Store.GetConnection(c.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *server) getConnection(w http.ResponseWriter, r *http.Request) {
	c, err := s.cfg.Store.GetConnection(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *server) listConnections(w http.ResponseWriter, _ *http.Request) {
	list, err := s.cfg.Store.ListConnections()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if list == nil {
		list = []*model.Connection{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	if err := s.cfg.Store.DeleteConnection(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
