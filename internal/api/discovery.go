package api

import (
	"errors"
	"net/http"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/orch"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/store"
)

// listDiscoveredVMs returns all discovered VMs (unknown machines that
// PXE-booted on the isolated network), most recently seen first.
func (s *server) listDiscoveredVMs(w http.ResponseWriter, r *http.Request) {
	vms, err := s.cfg.Store.ListDiscoveredVMs()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if vms == nil {
		vms = []*model.DiscoveredVM{}
	}
	writeJSON(w, http.StatusOK, vms)
}

// deleteDiscoveredVM dismisses a discovery record. The machine is
// re-discovered if it PXE-boots again.
func (s *server) deleteDiscoveredVM(w http.ResponseWriter, r *http.Request) {
	if _, err := s.cfg.Store.GetDiscoveredVM(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.cfg.Store.DeleteDiscoveredVM(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// adoptDiscoveredVM adopts a discovered VM per the requested mode:
//
//   - "rebind": the VM takes its original record's place in its original
//     deployment (restore-in-place).
//   - "new-deployment": the VM joins a fresh adopted deployment (default
//     name "adopted-<vm>"), or — with deploymentId — an existing adopted
//     one; the original deployment stays untouched (restore-alongside).
//
// On success the discovery agent reboots on its next heartbeat and the VM
// comes back up managed.
func (s *server) adoptDiscoveredVM(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Mode         string `json:"mode"`
		Name         string `json:"name,omitempty"`         // new-deployment: deployment name
		ConnectionID string `json:"connectionId,omitempty"` // new-deployment: where to look for the VM
		DeploymentID string `json:"deploymentId,omitempty"` // new-deployment: join this adopted deployment
	}
	if !readJSON(w, r, &body) {
		return
	}
	dv, err := s.cfg.Store.GetDiscoveredVM(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var res *orch.AdoptResult
	switch body.Mode {
	case "rebind":
		res, err = s.cfg.Orch.AdoptRebind(r.Context(), dv)
	case "new-deployment":
		res, err = s.cfg.Orch.AdoptNew(r.Context(), dv, body.Name, body.ConnectionID, body.DeploymentID)
	default:
		writeError(w, http.StatusBadRequest, "mode must be rebind or new-deployment")
		return
	}
	if err != nil {
		writeAdoptError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// writeAdoptError maps adoption failures: user-correctable preconditions and
// store conflicts keep their usual statuses; anything else is hypervisor
// trouble, surfaced as 502 with the cause (like writeConnectError) instead of
// an opaque 500.
func writeAdoptError(w http.ResponseWriter, err error) {
	var pre orch.ErrPrecondition
	var conflict store.ErrConflict
	switch {
	case errors.As(err, &pre):
		writeError(w, http.StatusConflict, pre.Error())
	case errors.As(err, &conflict):
		writeError(w, http.StatusConflict, conflict.Reason)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	default:
		writeError(w, http.StatusBadGateway, "adopting: "+err.Error())
	}
}
