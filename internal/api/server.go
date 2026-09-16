// Package api implements the controller's HTTP API and serves the web UI.
package api

import (
	_ "embed"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/buildinfo"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/orch"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/secrets"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/store"
)

//go:embed openapi.yaml
var openapiSpec []byte

//go:embed llms.txt
var llmsTxt []byte

// serviceDescLink advertises the machine-readable API description per RFC 8631,
// so a client (or LLM agent) given only the base URL can discover the OpenAPI
// spec instead of having to guess the path.
const serviceDescLink = `</api/v1/openapi.yaml>; rel="service-desc"; type="application/yaml"`

// Config wires the API's dependencies.
type Config struct {
	Store   *store.Store
	Secrets *secrets.Box
	Orch    *orch.Orchestrator
	// Password protects the UI/API when non-empty (UI-1). Empty = open access.
	Password string
	// APIKeys are static keys accepted for programmatic API access, in
	// addition to the session cookie. Like Password, configuring any key
	// turns the API gate on. Empty = no key auth.
	APIKeys []string
	// SecureCookies controls the Secure attribute on the session cookie:
	// "on" sets it always, "off" never, and empty (auto) sets it when the
	// request arrived over TLS or a reverse proxy said X-Forwarded-Proto:
	// https. The controller itself speaks plain HTTP (TLS is terminated at a
	// proxy, if at all), so an unconditional Secure would lock browsers out
	// on the trusted-network deployment.
	SecureCookies string
	// WebDist is the directory holding the built web UI; optional.
	WebDist string
	// ImagesDir holds the temp-OS boot artifacts (vmlinuz, initramfs.gz).
	ImagesDir string
	// BootURL is the controller's base URL as reachable from the isolated
	// network, used in generated iPXE scripts.
	BootURL string
	// Discovery boots unknown MACs (e.g. backup restores of GhostFleet VMs)
	// into the temp OS with a report-only token so they can be inspected and
	// adopted. Disabled, unknown MACs are told to give up quietly.
	Discovery bool
}

type server struct {
	cfg      Config
	sessions *sessionStore
}

// New returns the controller's root HTTP handler.
func New(cfg Config) http.Handler {
	s := &server{cfg: cfg, sessions: newSessionStore()}
	mux := http.NewServeMux()

	// Public endpoints.
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/v1/version", s.handleVersion)
	mux.HandleFunc("GET /api/v1/openapi.yaml", s.handleOpenAPI)
	// Discovery aliases at conventional well-known locations, so a tool or LLM
	// agent that only has the base URL can find the spec by probing.
	mux.HandleFunc("GET /openapi.yaml", s.handleOpenAPI)
	mux.HandleFunc("GET /llms.txt", s.handleLLMS)
	mux.HandleFunc("GET /api/v1/session", s.handleSessionStatus)
	mux.HandleFunc("POST /api/v1/session", s.handleLogin)
	mux.HandleFunc("DELETE /api/v1/session", s.handleLogout)

	// Netboot + agent plane (isolated network; token-gated, no session).
	mux.HandleFunc("GET /boot/script.ipxe", s.handleBootScript)
	mux.HandleFunc("GET /boot/{file}", s.handleBootFile)
	mux.HandleFunc("POST /agent/v1/register", s.handleAgentRegister)
	mux.HandleFunc("POST /agent/v1/heartbeat", s.handleAgentHeartbeat)
	mux.HandleFunc("POST /agent/v1/progress", s.handleAgentProgress)
	mux.HandleFunc("POST /agent/v1/discovery/report", s.handleDiscoveryReport)
	mux.HandleFunc("POST /agent/v1/discovery/heartbeat", s.handleDiscoveryHeartbeat)

	// Protected API.
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/connections", s.listConnections)
	api.HandleFunc("POST /api/v1/connections", s.createConnection)
	api.HandleFunc("GET /api/v1/connections/{id}", s.getConnection)
	api.HandleFunc("PUT /api/v1/connections/{id}", s.updateConnection)
	api.HandleFunc("DELETE /api/v1/connections/{id}", s.deleteConnection)
	api.HandleFunc("POST /api/v1/connections/{id}/validate", s.validateConnection)
	api.HandleFunc("GET /api/v1/connections/{id}/placement", s.listPlacementOptions)

	api.HandleFunc("GET /api/v1/profiles", s.listProfiles)
	api.HandleFunc("POST /api/v1/profiles", s.createProfile)
	api.HandleFunc("POST /api/v1/profiles/import", s.importProfile)
	api.HandleFunc("GET /api/v1/profiles/{id}", s.getProfile)
	api.HandleFunc("GET /api/v1/profiles/{id}/export", s.exportProfile)
	api.HandleFunc("PUT /api/v1/profiles/{id}", s.updateProfile)
	api.HandleFunc("DELETE /api/v1/profiles/{id}", s.deleteProfile)
	api.HandleFunc("GET /api/v1/profiles/{id}/versions", s.listProfileVersions)
	api.HandleFunc("GET /api/v1/profiles/{id}/versions/{version}", s.getProfileVersion)

	api.HandleFunc("GET /api/v1/deployments", s.listDeployments)
	api.HandleFunc("POST /api/v1/deployments", s.createDeployment)
	api.HandleFunc("GET /api/v1/deployments/{id}", s.getDeployment)
	api.HandleFunc("PUT /api/v1/deployments/{id}/spec", s.updateDeploymentSpec)
	api.HandleFunc("DELETE /api/v1/deployments/{id}", s.teardownDeployment)
	api.HandleFunc("GET /api/v1/deployments/{id}/runs", s.listRuns)
	api.HandleFunc("GET /api/v1/deployments/{id}/runs/{runId}/samples", s.listRunSamples)
	api.HandleFunc("POST /api/v1/deployments/{id}/deploy", s.deployDeployment)
	api.HandleFunc("GET /api/v1/deployments/{id}/conflicts", s.listConflicts)
	api.HandleFunc("POST /api/v1/deployments/{id}/power", s.powerDeployment)
	api.HandleFunc("POST /api/v1/deployments/{id}/fill", s.fillDeployment)
	api.HandleFunc("POST /api/v1/deployments/{id}/cancel", s.cancelDeployment)
	api.HandleFunc("GET /api/v1/deployments/{id}/vms", s.listDeploymentVMs)
	api.HandleFunc("GET /api/v1/deployments/{id}/vms/{vmId}/console", s.getVMConsole)

	api.HandleFunc("GET /api/v1/deployments/{id}/schedules", s.listSchedules)
	api.HandleFunc("POST /api/v1/deployments/{id}/schedules", s.createSchedule)
	api.HandleFunc("GET /api/v1/schedules/{id}", s.getSchedule)
	api.HandleFunc("PUT /api/v1/schedules/{id}", s.updateSchedule)
	api.HandleFunc("DELETE /api/v1/schedules/{id}", s.deleteSchedule)

	api.HandleFunc("GET /api/v1/discovered-vms", s.listDiscoveredVMs)
	api.HandleFunc("DELETE /api/v1/discovered-vms/{id}", s.deleteDiscoveredVM)
	api.HandleFunc("POST /api/v1/discovered-vms/{id}/adopt", s.adoptDiscoveredVM)

	mux.Handle("/api/v1/", s.requireAuth(api))

	// Web UI (SPA): serve files, fall back to index.html for client routes.
	if st, err := os.Stat(cfg.WebDist); err == nil && st.IsDir() {
		mux.Handle("/", spaHandler(cfg.WebDist))
	} else {
		slog.Warn("web UI directory not found, serving API only", "dir", cfg.WebDist)
	}
	return mux
}

// spaHandler serves static files and falls back to index.html so that
// client-side routes like /profiles work on reload.
//
// The existence check goes through the same http.Dir the file server uses,
// so the request path is never joined onto dist by hand: http.Dir rejects
// ".." segments and stays rooted under dist, which keeps the fallback from
// becoming a file-existence oracle for the rest of the filesystem.
func spaHandler(dist string) http.Handler {
	root := http.Dir(dist)
	fs := http.FileServer(root)
	index := filepath.Join(dist, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			f, err := root.Open(r.URL.Path)
			if err != nil {
				w.Header().Set("Link", serviceDescLink)
				http.ServeFile(w, r, index)
				return
			}
			f.Close()
		} else {
			w.Header().Set("Link", serviceDescLink)
		}
		fs.ServeHTTP(w, r)
	})
}

func (s *server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Link", serviceDescLink)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *server) handleLLMS(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(llmsTxt)
}

func (s *server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": buildinfo.Version})
}

func (s *server) handleOpenAPI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	w.Write(openapiSpec)
}

// --- JSON helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encoding response", "err", err)
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return false
	}
	return true
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeStoreError maps store errors to HTTP statuses.
func writeStoreError(w http.ResponseWriter, err error) {
	var conflict store.ErrConflict
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.As(err, &conflict):
		writeError(w, http.StatusConflict, conflict.Reason)
	default:
		slog.Error("store error", "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}
