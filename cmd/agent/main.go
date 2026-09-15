// The agent runs as PID 1 inside the PXE-booted temp OS on every generated
// VM (the initramfs init script execs it after bringing up eth0). It waits
// for IPv6 SLAAC, registers with the controller using the boot token from
// the kernel cmdline, heartbeats, and — when handed a work order — generates
// data with fio (see gen.go).
//
// All output goes to the console, so `govc vm.console -capture` screenshots
// double as remote debugging.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/buildinfo"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/datagen"
)

func logf(format string, args ...any) {
	fmt.Printf("[ghostfleet-agent] "+format+"\n", args...)
}

func main() {
	logf("%s starting", buildinfo.Version)

	cmdline := readCmdline()
	url := cmdline["ghostfleet.url"]
	token := cmdline["ghostfleet.token"]
	name := cmdline["ghostfleet.name"]
	if url == "" || token == "" {
		logf("FATAL: ghostfleet.url / ghostfleet.token missing from kernel cmdline")
		logf("cmdline: %v", cmdline)
		sleepForever()
	}
	logf("I am %s, controller %s", name, url)

	if !waitForIPv6("eth0", 120*time.Second) {
		logf("FATAL: no global IPv6 address on eth0 (is the RA/DHCPv6 service up?)")
		sleepForever()
	}

	client := &httpClient{
		base:      url,
		token:     token,
		http:      &http.Client{Timeout: 30 * time.Second},
		completed: map[string]bool{},
	}

	// Discovery mode: this machine is unknown to the controller (e.g. a
	// backup restore that came up with a fresh MAC). Inspect the disks for
	// GhostFleet identity, report, and await the operator's decision — after
	// adoption the controller tells us to reboot into managed life.
	if cmdline["ghostfleet.discover"] == "1" {
		runDiscovery(client) // never returns
	}

	// Register with retries — the controller may briefly be unreachable.
	for {
		resp, err := client.poll("register")
		if err == nil {
			logf("registered")
			client.dispatch(resp)
			break
		}
		logf("register failed (%v), retrying in 5s", err)
		time.Sleep(5 * time.Second)
	}

	for {
		time.Sleep(10 * time.Second)
		resp, err := client.poll("heartbeat")
		if err != nil {
			logf("heartbeat failed: %v", err)
			continue
		}
		client.dispatch(resp)
	}
}

// httpClient talks to the controller's token-gated agent API.
type httpClient struct {
	base  string
	token string
	http  *http.Client

	mu        sync.Mutex
	running   string          // run ID currently executing (guards re-dispatch)
	completed map[string]bool // run IDs finished this lifetime (guards re-run)
}

type agentResponse struct {
	VMName    string             `json:"vmName"`
	Action    string             `json:"action"`
	WorkOrder *datagen.WorkOrder `json:"workOrder"`
}

func (c *httpClient) poll(endpoint string) (*agentResponse, error) {
	status, body, err := c.post("/agent/v1/"+endpoint, map[string]string{"token": c.token})
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("status %d: %s", status, strings.TrimSpace(body))
	}
	var resp agentResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// dispatch starts a fill in the background if the controller handed one and
// it isn't already running.
func (c *httpClient) dispatch(resp *agentResponse) {
	if resp.Action != "fill" || resp.WorkOrder == nil {
		return
	}
	c.mu.Lock()
	// Skip if this run is already executing, or already finished in this
	// agent's lifetime — a controller that hasn't yet recorded our "done"
	// (e.g. a lost report) will keep handing the same work order back.
	if c.running == resp.WorkOrder.RunID || c.completed[resp.WorkOrder.RunID] {
		c.mu.Unlock()
		return
	}
	c.running = resp.WorkOrder.RunID
	c.mu.Unlock()

	wo := resp.WorkOrder
	go func() {
		runFill(c, wo)
		c.mu.Lock()
		c.completed[wo.RunID] = true
		c.running = ""
		c.mu.Unlock()
	}()
}

// reportProgress posts a progress sample. bytesTotal is the agent's own byte
// target (0 when unknown), which the controller prefers over its estimate.
func (c *httpClient) reportProgress(bytesWritten, bytesTotal int64, mbps float64) {
	c.post("/agent/v1/progress", map[string]any{
		"token": c.token, "bytesWritten": bytesWritten, "bytesTotal": bytesTotal, "mbps": mbps,
	})
}

func (c *httpClient) reportDone(bytesWritten int64) {
	c.postReliable("/agent/v1/progress", map[string]any{
		"token": c.token, "bytesWritten": bytesWritten, "done": true,
	})
}

func (c *httpClient) reportError(msg string) {
	c.postReliable("/agent/v1/progress", map[string]any{"token": c.token, "error": msg})
}

// postReliable retries a terminal status post (done/error) until the controller
// accepts it (2xx) or the budget is exhausted. A dropped terminal report would
// otherwise leave the controller's view of the VM stuck — and trip the run's
// stall detector — even though the fill actually finished. Progress samples
// stay best-effort (post); only the terminal status must land.
// terminalReportBackoff is the initial retry delay for postReliable (a var so
// tests can shrink it).
var terminalReportBackoff = time.Second

func (c *httpClient) postReliable(path string, body any) {
	backoff := terminalReportBackoff
	for attempt := 1; attempt <= 12; attempt++ {
		status, _, err := c.post(path, body)
		if err == nil && status >= 200 && status < 300 {
			return
		}
		logf("terminal report to %s not acked (attempt %d, status %d, err %v); retrying in %s",
			path, attempt, status, err, backoff)
		time.Sleep(backoff)
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
	logf("WARN: gave up reporting to %s after retries", path)
}

func (c *httpClient) post(path string, body any) (int, string, error) {
	payload, _ := json.Marshal(body)
	resp, err := c.http.Post(c.base+path, "application/json", bytes.NewReader(payload))
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.String(), nil
}

// readCmdline parses /proc/cmdline into key=value pairs.
func readCmdline() map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		logf("reading /proc/cmdline: %v", err)
		return out
	}
	for _, field := range strings.Fields(string(raw)) {
		if k, v, ok := strings.Cut(field, "="); ok {
			out[k] = v
		} else {
			out[field] = ""
		}
	}
	return out
}

// waitForIPv6 waits until the interface holds a non-link-local IPv6 address
// (SLAAC from the controller's router advertisements).
func waitForIPv6(ifname string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		iface, err := net.InterfaceByName(ifname)
		if err == nil {
			addrs, _ := iface.Addrs()
			for _, a := range addrs {
				ipnet, ok := a.(*net.IPNet)
				if !ok || ipnet.IP.To4() != nil {
					continue
				}
				if ipnet.IP.IsGlobalUnicast() { // ULA counts as global unicast
					logf("eth0 has %s", ipnet.IP)
					return true
				}
			}
		}
		time.Sleep(time.Second)
	}
	return false
}

// sleepForever keeps PID 1 alive so the console stays inspectable.
func sleepForever() {
	for {
		time.Sleep(time.Hour)
	}
}
