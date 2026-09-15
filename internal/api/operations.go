package api

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/orch"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/store"
)

// validateConnection opens the hypervisor connection and reports
// product/version — the UI's "Test connection" button.
func (s *server) validateConnection(w http.ResponseWriter, r *http.Request) {
	driver, err := s.cfg.Orch.OpenDriver(r.Context(), r.PathValue("id"))
	if err != nil {
		writeConnectError(w, err)
		return
	}
	defer driver.Close()
	info, err := driver.Info(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// listPlacementOptions enumerates clusters/datastores/networks for the
// deploy-time placement picker (PRO-9).
func (s *server) listPlacementOptions(w http.ResponseWriter, r *http.Request) {
	driver, err := s.cfg.Orch.OpenDriver(r.Context(), r.PathValue("id"))
	if err != nil {
		writeConnectError(w, err)
		return
	}
	defer driver.Close()
	opts, err := driver.ListPlacement(r.Context())
	if err != nil {
		writeConnectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, opts)
}

// writeConnectError distinguishes "no such connection" from hypervisor
// trouble, which is reported as 502 with the cause.
func writeConnectError(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeStoreError(w, err)
		return
	}
	writeError(w, http.StatusBadGateway, "hypervisor: "+err.Error())
}

// deployDeployment starts the async reconcile job (deploy or scale-up). The
// optional body {onConflict} chooses how to handle a target VM name already
// owned by another deployment (abort default | adopt | clean).
func (s *server) deployDeployment(w http.ResponseWriter, r *http.Request) {
	onConflict := model.OnConflictAbort
	if r.ContentLength != 0 {
		var body struct {
			OnConflict string `json:"onConflict"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		if body.OnConflict != "" {
			onConflict = body.OnConflict
		}
	}
	switch onConflict {
	case model.OnConflictAbort, model.OnConflictAdopt, model.OnConflictClean:
	default:
		writeError(w, http.StatusBadRequest, "onConflict must be abort, adopt or clean")
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
	run, err := s.cfg.Orch.StartDeploy(d, onConflict)
	if err != nil {
		writeJobError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

// listConflicts reports target VM names already on the hypervisor owned by
// another/unknown deployment, so the UI can warn before deploying.
func (s *server) listConflicts(w http.ResponseWriter, r *http.Request) {
	d, err := s.cfg.Store.GetDeployment(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	conflicts, err := s.cfg.Orch.DetectConflicts(d)
	if err != nil {
		writeConnectError(w, err)
		return
	}
	if conflicts == nil {
		conflicts = []model.VMConflict{}
	}
	writeJSON(w, http.StatusOK, conflicts)
}

// teardownDeployment starts the async teardown job. The deployment record
// and its run history are kept (PRO-7).
func (s *server) teardownDeployment(w http.ResponseWriter, r *http.Request) {
	d, err := s.cfg.Store.GetDeployment(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if d.Status == model.DeploymentDeleted {
		writeError(w, http.StatusConflict, "deployment is already deleted")
		return
	}
	vms, err := s.cfg.Store.ListManagedVMs(d.ID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if len(vms) == 0 { // nothing on the hypervisor — finish synchronously
		if err := s.cfg.Store.MarkDeploymentDeleted(d.ID); err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	run, err := s.cfg.Orch.StartTeardown(d)
	if err != nil {
		writeJobError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

// fillDeployment starts an initial-fill, incremental or verify run (verify
// reads all generated data back and checks it against the on-disk checksum
// manifests — e.g. to prove a backup restore round-tripped intact).
func (s *server) fillDeployment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Type string `json:"type"` // initial-fill | incremental | verify
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Type != model.RunInitialFill && body.Type != model.RunIncremental && body.Type != model.RunVerify {
		writeError(w, http.StatusBadRequest, "type must be initial-fill, incremental or verify")
		return
	}
	d, err := s.cfg.Store.GetDeployment(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if d.Status != model.DeploymentReady {
		writeError(w, http.StatusConflict, "deployment must be ready (deploy it first)")
		return
	}
	run, err := s.cfg.Orch.StartFill(d, body.Type)
	if err != nil {
		writeJobError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}

// cancelDeployment aborts the deployment's currently running job (deploy,
// scale-up, teardown, power or fill). Returns 409 if nothing is running.
func (s *server) cancelDeployment(w http.ResponseWriter, r *http.Request) {
	if _, err := s.cfg.Store.GetDeployment(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	if err := s.cfg.Orch.Cancel(r.PathValue("id")); err != nil {
		writeJobError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// listDeploymentVMs returns the managed VMs of a deployment.
func (s *server) listDeploymentVMs(w http.ResponseWriter, r *http.Request) {
	if _, err := s.cfg.Store.GetDeployment(r.PathValue("id")); err != nil {
		writeStoreError(w, err)
		return
	}
	vms, err := s.cfg.Store.ListManagedVMs(r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if vms == nil {
		vms = []*model.ManagedVM{}
	}
	writeJSON(w, http.StatusOK, vms)
}

// listRunSamples returns a run's throughput time series for charting.
func (s *server) listRunSamples(w http.ResponseWriter, r *http.Request) {
	samples, err := s.cfg.Store.ListRunSamples(r.PathValue("runId"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if samples == nil {
		samples = []store.RunSample{}
	}
	writeJSON(w, http.StatusOK, samples)
}

// getVMConsole returns one boot session of a VM's serial console. The serial
// log accumulates across reboots; it is split on the agent's startup marker
// so each boot is a separate session (?session=N, 1-based; default latest).
func (s *server) getVMConsole(w http.ResponseWriter, r *http.Request) {
	vm, err := s.cfg.Store.GetManagedVM(r.PathValue("vmId"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	d, err := s.cfg.Store.GetDeployment(vm.DeploymentID)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	driver, err := s.cfg.Orch.OpenDriver(r.Context(), d.ConnectionID)
	if err != nil {
		writeConnectError(w, err)
		return
	}
	defer driver.Close()
	text, err := driver.ReadConsole(r.Context(), vm.Ref)
	if err != nil {
		writeError(w, http.StatusBadGateway, "reading console: "+err.Error())
		return
	}

	sessions := splitConsoleSessions(text)
	sel := len(sessions) // default: latest
	if q := r.URL.Query().Get("session"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n >= 1 && n <= len(sessions) {
			sel = n
		}
	}
	content := ""
	if sel >= 1 && sel <= len(sessions) {
		content = sessions[sel-1].Text
	}
	meta := make([]map[string]any, len(sessions))
	for i, s := range sessions {
		meta[i] = map[string]any{"n": i + 1, "startedAt": s.StartedAt, "partial": s.Partial}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sessionCount": len(sessions),
		"session":      sel,
		"sessions":     meta,
		"console":      content,
	})
}

// consoleSession is one boot's serial output plus its wall-clock start.
type consoleSession struct {
	Text      string
	StartedAt string // from the kernel RTC line; empty if not found
	Partial   bool   // true for a leading chunk cut by the tail read
}

// bootBanner marks the start of a boot in the serial log (kernel banner).
const bootBanner = "Linux version"

// rtcLine carries the boot wall-clock, e.g.
// "rtc_cmos 00:01: setting system clock to 2026-06-15T13:30:52 UTC (...)".
var rtcRe = regexp.MustCompile(`setting system clock to (\S+) UTC`)

// splitConsoleSessions splits the serial log into per-boot sessions on the
// kernel boot banner (so a boot's kernel output stays with that boot, not the
// previous one) and reads each boot's wall-clock from the kernel RTC line.
// A leading chunk before the first banner (the tail of an older boot cut by
// the read cap) is kept as a partial session.
func splitConsoleSessions(text string) []consoleSession {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	var sessions []consoleSession
	var cur strings.Builder
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		body := cur.String()
		ts := ""
		if m := rtcRe.FindStringSubmatch(body); m != nil {
			ts = m[1]
		}
		// A chunk without the kernel banner is the tail of an older boot
		// that the read cap cut off — mark it partial.
		sessions = append(sessions, consoleSession{
			Text: body, StartedAt: ts, Partial: !strings.Contains(body, bootBanner),
		})
		cur.Reset()
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, bootBanner) {
			flush() // the banner starts a new boot; end the previous one
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
	}
	flush()
	return sessions
}

func writeJobError(w http.ResponseWriter, err error) {
	var busy orch.ErrBusy
	var notRunning orch.ErrNotRunning
	var pre orch.ErrPrecondition
	switch {
	case errors.As(err, &busy):
		writeError(w, http.StatusConflict, busy.Error())
	case errors.As(err, &notRunning):
		writeError(w, http.StatusConflict, notRunning.Error())
	case errors.As(err, &pre):
		// A user-correctable precondition (e.g. incremental before any fill) —
		// surface the explanatory message rather than a generic 500.
		writeError(w, http.StatusConflict, pre.Error())
	default:
		writeStoreError(w, err)
	}
}
