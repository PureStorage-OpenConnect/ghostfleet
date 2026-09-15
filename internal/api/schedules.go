package api

import (
	"net/http"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

// scheduleRequest creates or replaces a schedule definition. Enabled defaults
// to true on create; on update it is taken as sent (the pause/resume toggle).
type scheduleRequest struct {
	Action   string `json:"action"`
	Kind     string `json:"kind"`
	Spec     string `json:"spec"`
	Timezone string `json:"timezone,omitempty"`
	Enabled  *bool  `json:"enabled,omitempty"`
}

// validate builds the schedule fields from the request and computes the next
// fire time. Returns an error message for the client, or "".
func (req *scheduleRequest) validate(sc *model.Schedule) string {
	sc.Action, sc.Kind, sc.Spec, sc.Timezone = req.Action, req.Kind, req.Spec, req.Timezone
	sc.Enabled = req.Enabled == nil || *req.Enabled
	if err := sc.Validate(); err != nil {
		return err.Error()
	}
	sc.NextRunAt = nil
	if sc.Enabled {
		next, ok := sc.NextAfter(time.Now())
		if !ok {
			// Only a one-shot in the past has no next occurrence.
			return "once: the fire time must be in the future"
		}
		sc.NextRunAt = &next
	}
	return ""
}

// listSchedules returns a deployment's schedules.
func (s *server) listSchedules(w http.ResponseWriter, r *http.Request) {
	if _, err := s.cfg.Store.GetDeployment(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	list, err := s.cfg.Store.ListSchedules(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if list == nil {
		list = []*model.Schedule{}
	}
	writeJSON(w, http.StatusOK, list)
}

// createSchedule adds a timer to a deployment.
func (s *server) createSchedule(w http.ResponseWriter, r *http.Request) {
	var req scheduleRequest
	if !readJSON(w, r, &req) {
		return
	}
	d, err := s.cfg.Store.GetDeployment(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if d.Status == model.DeploymentDeleted || d.Status == model.DeploymentDeleting {
		writeError(w, http.StatusConflict, "deployment is deleted")
		return
	}
	sc := &model.Schedule{DeploymentID: d.ID}
	if msg := req.validate(sc); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	created, err := s.cfg.Store.CreateSchedule(sc)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// getSchedule returns one schedule.
func (s *server) getSchedule(w http.ResponseWriter, r *http.Request) {
	sc, err := s.cfg.Store.GetSchedule(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

// updateSchedule replaces a schedule's definition and recomputes its next
// fire time (also the enable/disable toggle).
func (s *server) updateSchedule(w http.ResponseWriter, r *http.Request) {
	var req scheduleRequest
	if !readJSON(w, r, &req) {
		return
	}
	sc, err := s.cfg.Store.GetSchedule(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if msg := req.validate(sc); msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	if err := s.cfg.Store.UpdateSchedule(sc); err != nil {
		writeStoreError(w, err)
		return
	}
	updated, err := s.cfg.Store.GetSchedule(sc.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// deleteSchedule removes a schedule.
func (s *server) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	if err := s.cfg.Store.DeleteSchedule(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
