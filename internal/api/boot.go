package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/datagen"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

// The netboot and agent endpoints below are served WITHOUT session auth:
// they are reached over the isolated network by firmware, iPXE and the
// temp-OS agent. Authorization is the per-VM boot token (ARCHITECTURE.md §7),
// matched via MAC for the initial script request.

// handleBootScript returns the per-VM iPXE script. iPXE (with our embedded
// chainload script) requests it with its MAC; we match the managed VM and
// hand out kernel/initramfs plus the VM's boot token on the cmdline.
//
// An unknown MAC — typically a backup restore of a GhostFleet VM, which comes
// up with a fresh MAC — gets a discovery boot instead (unless discovery is
// disabled): the same temp OS with a report-only discovery token, so its
// agent can inspect the disks for GhostFleet identity and the operator can
// adopt the VM. Discovery tokens never yield work orders (ARCHITECTURE.md §7).
func (s *server) handleBootScript(w http.ResponseWriter, r *http.Request) {
	mac := r.URL.Query().Get("mac")
	if mac == "" {
		writeError(w, http.StatusBadRequest, "mac query parameter required")
		return
	}
	vm, err := s.cfg.Store.GetManagedVMByMAC(mac)
	if err != nil {
		s.handleDiscoveryBoot(w, mac)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, `#!ipxe
echo GhostFleet: booting %s
kernel %s/boot/vmlinuz console=tty0 console=ttyS0 ghostfleet.url=%s ghostfleet.token=%s ghostfleet.name=%s
initrd %s/boot/initramfs.gz
boot
`, vm.Name, s.cfg.BootURL, s.cfg.BootURL, vm.BootToken, vm.Name, s.cfg.BootURL)
}

// handleDiscoveryBoot serves the discovery iPXE script for an unknown MAC,
// recording (or re-touching) the discovered VM. With discovery disabled the
// machine is told to give up quietly instead of boot-looping.
func (s *server) handleDiscoveryBoot(w http.ResponseWriter, mac string) {
	w.Header().Set("Content-Type", "text/plain")
	if !s.cfg.Discovery {
		fmt.Fprintf(w, "#!ipxe\necho GhostFleet: MAC %s is not a managed VM\nsleep 10\nexit\n", mac)
		return
	}
	dv, err := s.cfg.Store.EnsureDiscoveredVM(mac)
	if err != nil {
		fmt.Fprintf(w, "#!ipxe\necho GhostFleet: discovery failed for MAC %s\nsleep 10\nexit\n", mac)
		return
	}
	fmt.Fprintf(w, `#!ipxe
echo GhostFleet: unknown MAC %s - booting for discovery
kernel %s/boot/vmlinuz console=tty0 console=ttyS0 ghostfleet.url=%s ghostfleet.token=%s ghostfleet.discover=1 ghostfleet.name=discovery
initrd %s/boot/initramfs.gz
boot
`, mac, s.cfg.BootURL, s.cfg.BootURL, dv.Token, s.cfg.BootURL)
}

// handleBootFile serves kernel/initramfs artifacts from the images dir.
func (s *server) handleBootFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	switch name {
	case "vmlinuz", "initramfs.gz":
		http.ServeFile(w, r, filepath.Join(s.cfg.ImagesDir, name))
	default:
		writeError(w, http.StatusNotFound, "unknown boot artifact")
	}
}

// agentAuth resolves the VM behind an agent request's token.
func (s *server) agentAuth(w http.ResponseWriter, r *http.Request) *model.ManagedVM {
	var body struct {
		Token string `json:"token"`
	}
	if !readJSON(w, r, &body) {
		return nil
	}
	vm, err := s.cfg.Store.GetManagedVMByToken(body.Token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unknown boot token")
		return nil
	}
	return vm
}

// agentResponse is returned by register and heartbeat: the current action
// and, when action is "fill", the work order to execute.
type agentResponse struct {
	VMName    string `json:"vmName"`
	Action    string `json:"action"` // idle | fill
	WorkOrder any    `json:"workOrder,omitempty"`
}

// handleAgentRegister is the agent's first call after boot; like heartbeat
// it returns the current action so the agent can start work immediately.
func (s *server) handleAgentRegister(w http.ResponseWriter, r *http.Request) {
	s.agentRespond(w, r)
}

// handleAgentHeartbeat refreshes liveness and returns the current action.
func (s *server) handleAgentHeartbeat(w http.ResponseWriter, r *http.Request) {
	s.agentRespond(w, r)
}

func (s *server) agentRespond(w http.ResponseWriter, r *http.Request) {
	vm := s.agentAuth(w, r)
	if vm == nil {
		return
	}
	if err := s.cfg.Store.TouchAgent(vm.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	resp := agentResponse{VMName: vm.Name, Action: "idle"}
	if action, wo := s.cfg.Orch.WorkOrderForToken(vm); action == "fill" {
		resp.Action = "fill"
		resp.WorkOrder = wo
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleAgentProgress records a generation progress sample from an agent.
func (s *server) handleAgentProgress(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token        string  `json:"token"`
		BytesWritten int64   `json:"bytesWritten"`
		BytesTotal   int64   `json:"bytesTotal"` // agent's own target; 0 = keep the controller's estimate
		MBps         float64 `json:"mbps"`
		Done         bool    `json:"done"`
		Error        string  `json:"error"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	vm, err := s.cfg.Store.GetManagedVMByToken(body.Token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unknown boot token")
		return
	}
	if body.BytesTotal > 0 {
		if err := s.cfg.Store.SetFillTotal(vm.ID, body.BytesTotal); err != nil {
			writeStoreError(w, err)
			return
		}
	}
	if err := s.cfg.Store.ReportFillProgress(vm.ID, body.BytesWritten, body.MBps, body.Done, body.Error); err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// discoveryAuth resolves the discovered VM behind a discovery-token request.
func (s *server) discoveryAuth(w http.ResponseWriter, token string) *model.DiscoveredVM {
	dv, err := s.cfg.Store.GetDiscoveredVMByToken(token)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unknown discovery token")
		return nil
	}
	return dv
}

// discoveryAction tells the discovery agent what to do next: idle while the
// VM awaits an operator decision, reboot once it is adopted — the reboot
// PXE-boots it again and its MAC now resolves to a managed VM.
func discoveryAction(dv *model.DiscoveredVM) string {
	if dv.Status == model.DiscoveredAdopted {
		return "reboot"
	}
	return "idle"
}

// handleDiscoveryReport stores a discovery agent's disk-inspection result.
// This is all a discovery token authorizes (plus heartbeats): it never hands
// out work orders, keeping the §7 invariant that a stray machine on the
// isolated network can't fetch anything actionable.
func (s *server) handleDiscoveryReport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string                  `json:"token"`
		Disks []datagen.DiscoveryDisk `json:"disks"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	dv := s.discoveryAuth(w, body.Token)
	if dv == nil {
		return
	}
	report, err := json.Marshal(datagen.DiscoveryReport{Disks: body.Disks})
	if err != nil {
		writeError(w, http.StatusBadRequest, "unencodable report")
		return
	}
	if err := s.cfg.Store.SetDiscoveredInspection(dv.ID, string(report)); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"action": discoveryAction(dv)})
}

// handleDiscoveryHeartbeat refreshes a discovery agent's liveness and tells
// it whether to reboot (after adoption).
func (s *server) handleDiscoveryHeartbeat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	dv := s.discoveryAuth(w, body.Token)
	if dv == nil {
		return
	}
	if err := s.cfg.Store.TouchDiscoveredAgent(dv.ID); err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"action": discoveryAction(dv)})
}

// powerDeployment starts the async power on/off job.
func (s *server) powerDeployment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On bool `json:"on"`
	}
	if !readJSON(w, r, &body) {
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
	run, err := s.cfg.Orch.StartPower(d, body.On)
	if err != nil {
		writeJobError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, run)
}
