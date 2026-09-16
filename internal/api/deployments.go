package api

import (
	"fmt"
	"net/http"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

// deploymentRequest creates a deployment from a profile version plus
// optional deploy-time overrides (PRO-8/9).
type deploymentRequest struct {
	Name           string             `json:"name"`
	ProfileID      string             `json:"profileId"`
	ProfileVersion int                `json:"profileVersion,omitempty"` // 0 = current
	ConnectionID   string             `json:"connectionId"`
	Spec           *model.ProfileSpec `json:"spec,omitempty"` // overrides the profile spec entirely
	Placement      model.Placement    `json:"placement,omitempty"`
}

func (s *server) createDeployment(w http.ResponseWriter, r *http.Request) {
	var req deploymentRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.Name == "" || req.ProfileID == "" || req.ConnectionID == "" {
		writeError(w, http.StatusBadRequest, "name, profileId and connectionId are required")
		return
	}
	if _, err := s.cfg.Store.GetConnection(req.ConnectionID); err != nil {
		writeError(w, http.StatusBadRequest, "connectionId: no such connection")
		return
	}

	// Resolve the effective config: explicit spec override, or the snapshot
	// of the requested (default: current) profile version.
	profile, err := s.cfg.Store.GetProfile(req.ProfileID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "profileId: no such profile")
		return
	}
	version := req.ProfileVersion
	if version == 0 {
		version = profile.CurrentVersion
	}
	spec := req.Spec
	if spec == nil {
		v, err := s.cfg.Store.GetProfileVersion(req.ProfileID, version)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		spec = &v.Spec
	}
	spec.ApplyDefaults()
	if err := spec.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Placement == nil {
		req.Placement = model.Placement{}
	}

	d, err := s.cfg.Store.CreateDeployment(&model.Deployment{
		Name: req.Name, ProfileID: req.ProfileID, ProfileVersion: version,
		ConnectionID: req.ConnectionID, Spec: *spec, Placement: req.Placement,
	})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

// updateDeploymentSpec replaces the effective config. Shrinking the fleet
// is rejected (PRO-6: grow or full teardown, never shrink).
func (s *server) updateDeploymentSpec(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Spec model.ProfileSpec `json:"spec"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	d, err := s.cfg.Store.GetDeployment(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	req.Spec.ApplyDefaults()
	if err := req.Spec.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if msg := shrinkCheck(d.Spec, req.Spec); msg != "" {
		writeError(w, http.StatusConflict, msg)
		return
	}
	if err := s.cfg.Store.UpdateDeploymentSpec(d.ID, req.Spec); err != nil {
		writeStoreError(w, err)
		return
	}
	updated, err := s.cfg.Store.GetDeployment(d.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// shrinkCheck enforces the grow-only rule on the dimensions that map to
// hypervisor objects or written data.
func shrinkCheck(old, new model.ProfileSpec) string {
	type dim struct {
		name     string
		old, new int
	}
	for _, d := range []dim{
		{"vmCount", old.VMCount, new.VMCount},
		{"disksPerVM", old.DisksPerVM, new.DisksPerVM},
		{"diskSizeGiB", old.DiskSizeGiB, new.DiskSizeGiB},
		{"dataPerDiskGiB", old.DataPerDiskGiB, new.DataPerDiskGiB},
	} {
		if d.new < d.old {
			return fmt.Sprintf("deployments cannot shrink: %s %d -> %d (delete the deployment instead)",
				d.name, d.old, d.new)
		}
	}
	return ""
}

func (s *server) getDeployment(w http.ResponseWriter, r *http.Request) {
	d, err := s.cfg.Store.GetDeployment(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if filled, err := s.cfg.Store.DeploymentFilled(d.ID); err == nil {
		d.Filled = filled
	}
	d.Running = s.cfg.Orch.IsActive(d.ID)
	writeJSON(w, http.StatusOK, d)
}

func (s *server) listDeployments(w http.ResponseWriter, _ *http.Request) {
	list, err := s.cfg.Store.ListDeployments()
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if list == nil {
		list = []*model.Deployment{}
	}
	filled, _ := s.cfg.Store.FilledDeploymentIDs()
	for _, d := range list {
		d.Filled = filled[d.ID]
		d.Running = s.cfg.Orch.IsActive(d.ID)
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *server) listRuns(w http.ResponseWriter, r *http.Request) {
	if _, err := s.cfg.Store.GetDeployment(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	list, err := s.cfg.Store.ListRuns(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if list == nil {
		list = []*model.Run{}
	}
	writeJSON(w, http.StatusOK, list)
}
