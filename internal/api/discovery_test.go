package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/hypervisor"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/hypervisor/fake"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/orch"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/secrets"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/store"
)

// deployFleet creates profile + connection + deployment and reconciles it,
// returning the deployment ID and its first VM (name, mac).
func deployFleet(t *testing.T, srv *httptest.Server, client *http.Client, name string) (string, string, string) {
	t.Helper()
	_, p := doJSON(t, client, "POST", srv.URL+"/api/v1/profiles",
		map[string]any{"name": name + "-prof", "spec": specBody})
	_, c := doJSON(t, client, "POST", srv.URL+"/api/v1/connections", map[string]any{
		"name": name + "-vc", "plugin": "vsphere", "endpoint": "https://vc.lab",
		"username": "admin", "secret": "x",
	})
	_, d := doJSON(t, client, "POST", srv.URL+"/api/v1/deployments", map[string]any{
		"name": name, "profileId": p["id"], "connectionId": c["id"],
		"placement": map[string]string{"cluster": "C0", "datastore": "LocalDS_0"},
	})
	did := d["id"].(string)
	doJSON(t, client, "POST", srv.URL+"/api/v1/deployments/"+did+"/deploy", nil)
	waitStatus(t, client, srv.URL, did, "ready")
	_, vms := doJSONList(t, client, srv.URL+"/api/v1/deployments/"+did+"/vms")
	return did, vms[0]["name"].(string), vms[0]["mac"].(string)
}

// discoveryToken fetches the boot script for a MAC and returns the discovery
// token it carries.
func discoveryToken(t *testing.T, srv *httptest.Server, mac string) string {
	t.Helper()
	res, err := http.Get(srv.URL + "/boot/script.ipxe?mac=" + mac)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("boot script: %v %d", err, res.StatusCode)
	}
	body := new(bytes.Buffer)
	body.ReadFrom(res.Body)
	res.Body.Close()
	if !strings.Contains(body.String(), "ghostfleet.discover=1") {
		t.Fatalf("expected discovery script, got: %s", body.String())
	}
	for _, f := range strings.Fields(body.String()) {
		if strings.HasPrefix(f, "ghostfleet.token=") {
			return strings.TrimPrefix(f, "ghostfleet.token=")
		}
	}
	t.Fatal("no token in discovery script")
	return ""
}

// inspectionBody builds a discovery report whose identity marker points at
// the given deployment/VM.
func inspectionBody(token, deploymentID, deploymentName, vmName string) map[string]any {
	identity := map[string]any{
		"deploymentId": deploymentID, "deploymentName": deploymentName,
		"vmName": vmName, "diskIndex": 0, "spec": specBody,
	}
	return map[string]any{
		"token": token,
		"disks": []map[string]any{
			{"device": "sdb", "sizeGiB": 100, "filesystem": "xfs", "identity": identity,
				"runId": "run-original", "runBytes": 1 << 30, "manifest": true, "fileCount": 50},
			{"device": "sdc", "sizeGiB": 100, "filesystem": "xfs", "identity": identity,
				"runId": "run-original", "runBytes": 1 << 30, "manifest": true, "fileCount": 50},
		},
	}
}

func TestDiscoveryAdoptNewDeployment(t *testing.T) {
	srv, client, driver := testServerWithDriver(t, "")
	did, vmName, _ := deployFleet(t, srv, client, "adopt-src")

	// A restore-alongside copy: same disks, new name, fresh MAC.
	mac := "de:ad:be:ef:10:01"
	driver.AddRawVM(vmName+"-restored", mac, 2)

	// The unknown MAC gets a discovery boot; the agent reports its findings.
	token := discoveryToken(t, srv, mac)
	anon := &http.Client{}
	resp, rep := doJSON(t, anon, "POST", srv.URL+"/agent/v1/discovery/report",
		inspectionBody(token, did, "adopt-src", vmName))
	if resp.StatusCode != 200 || rep["action"] != "idle" {
		t.Fatalf("report: %d %v", resp.StatusCode, rep)
	}
	resp, _ = doJSON(t, anon, "POST", srv.URL+"/agent/v1/discovery/report",
		map[string]any{"token": "wrong", "disks": []any{}})
	if resp.StatusCode != 401 {
		t.Fatalf("bad discovery token: %d", resp.StatusCode)
	}

	// The discovered VM shows up on the management API.
	_, discovered := doJSONList(t, client, srv.URL+"/api/v1/discovered-vms")
	if len(discovered) != 1 || discovered[0]["mac"] != mac || discovered[0]["status"] != "new" {
		t.Fatalf("discovered list: %v", discovered)
	}
	dvID := discovered[0]["id"].(string)

	// Adopt into a new deployment (connection defaults to the original's).
	resp, res := doJSON(t, client, "POST", srv.URL+"/api/v1/discovered-vms/"+dvID+"/adopt",
		map[string]any{"mode": "new-deployment"})
	if resp.StatusCode != 200 {
		t.Fatalf("adopt: %d %v", resp.StatusCode, res)
	}
	adopted := res["deployment"].(map[string]any)
	if adopted["origin"] != "adopted" || adopted["status"] != "ready" {
		t.Fatalf("adopted deployment: %v", adopted)
	}
	vm := res["vm"].(map[string]any)
	if vm["name"] != vmName+"-restored" || vm["mac"] != mac {
		t.Fatalf("adopted vm: %v", vm)
	}
	// The discovery report already told us what the disks hold.
	if vm["dataState"] != "filled" || vm["dataRunId"] != "run-original" || vm["dataManifest"] != true {
		t.Fatalf("adopted vm data state: %v", vm)
	}
	adoptedID := adopted["id"].(string)

	// The original deployment is untouched.
	_, vms := doJSONList(t, client, srv.URL+"/api/v1/deployments/"+did+"/vms")
	if len(vms) != 4 {
		t.Fatalf("original deployment changed: %d VMs", len(vms))
	}

	// The discovery agent is told to reboot on its next heartbeat.
	resp, hb := doJSON(t, anon, "POST", srv.URL+"/agent/v1/discovery/heartbeat", map[string]string{"token": token})
	if resp.StatusCode != 200 || hb["action"] != "reboot" {
		t.Fatalf("heartbeat after adopt: %d %v", resp.StatusCode, hb)
	}

	// After the reboot the MAC resolves to a managed boot.
	sres, _ := http.Get(srv.URL + "/boot/script.ipxe?mac=" + mac)
	script := new(bytes.Buffer)
	script.ReadFrom(sres.Body)
	sres.Body.Close()
	if !strings.Contains(script.String(), "ghostfleet.name="+vmName+"-restored") {
		t.Fatalf("adopted VM should get a managed boot: %s", script.String())
	}

	// Reconcile is not applicable to adopted deployments…
	resp, body := doJSON(t, client, "POST", srv.URL+"/api/v1/deployments/"+adoptedID+"/deploy", nil)
	if resp.StatusCode != 409 {
		t.Fatalf("deploy on adopted deployment: %d %v", resp.StatusCode, body)
	}
	// …but verify and incremental are (no prior fill needed: data was restored).
	resp, run := doJSON(t, client, "POST", srv.URL+"/api/v1/deployments/"+adoptedID+"/fill",
		map[string]string{"type": "verify"})
	if resp.StatusCode != 202 || run["type"] != "verify" {
		t.Fatalf("verify on adopted deployment: %d %v", resp.StatusCode, run)
	}
	doJSON(t, client, "POST", srv.URL+"/api/v1/deployments/"+adoptedID+"/cancel", nil)

	// Adopting the same discovered VM twice is refused.
	resp, body = doJSON(t, client, "POST", srv.URL+"/api/v1/discovered-vms/"+dvID+"/adopt",
		map[string]any{"mode": "new-deployment"})
	if resp.StatusCode != 409 {
		t.Fatalf("second adopt: %d %v", resp.StatusCode, body)
	}
}

func TestDiscoveryAdoptRebind(t *testing.T) {
	srv, client, driver := testServerWithDriver(t, "")
	did, vmName, oldMAC := deployFleet(t, srv, client, "rebind-src")

	// Restore-in-place: same VM name, fresh MAC (the original still exists on
	// the hypervisor here, so the adoption must warn about it).
	mac := "de:ad:be:ef:20:01"
	driver.AddRawVM(vmName, mac, 2)

	token := discoveryToken(t, srv, mac)
	anon := &http.Client{}
	doJSON(t, anon, "POST", srv.URL+"/agent/v1/discovery/report",
		inspectionBody(token, did, "rebind-src", vmName))
	_, discovered := doJSONList(t, client, srv.URL+"/api/v1/discovered-vms")
	dvID := discovered[0]["id"].(string)

	resp, res := doJSON(t, client, "POST", srv.URL+"/api/v1/discovered-vms/"+dvID+"/adopt",
		map[string]any{"mode": "rebind"})
	if resp.StatusCode != 200 {
		t.Fatalf("rebind: %d %v", resp.StatusCode, res)
	}
	if w, _ := res["warning"].(string); !strings.Contains(w, "unmanaged") {
		t.Fatalf("expected still-exists warning, got %v", res["warning"])
	}

	// The record now points at the restored VM: same name, new MAC.
	_, vms := doJSONList(t, client, srv.URL+"/api/v1/deployments/"+did+"/vms")
	found := false
	for _, vm := range vms {
		if vm["name"] == vmName {
			found = true
			if vm["mac"] != mac {
				t.Fatalf("rebind did not update the MAC: %v", vm)
			}
		}
	}
	if !found || len(vms) != 4 {
		t.Fatalf("deployment VM records changed unexpectedly: %v", vms)
	}

	// The old MAC no longer resolves to a managed VM.
	sres, _ := http.Get(srv.URL + "/boot/script.ipxe?mac=" + oldMAC)
	script := new(bytes.Buffer)
	script.ReadFrom(sres.Body)
	sres.Body.Close()
	if !strings.Contains(script.String(), "ghostfleet.discover=1") {
		t.Fatalf("old MAC should now be unknown: %s", script.String())
	}
}

func TestDiscoveryNotAdoptableWithoutIdentity(t *testing.T) {
	srv, client, driver := testServerWithDriver(t, "")
	driver.AddRawVM("stray", "de:ad:be:ef:30:01", 1)

	token := discoveryToken(t, srv, "de:ad:be:ef:30:01")
	anon := &http.Client{}
	// A stray machine: disks, but no GhostFleet markers.
	doJSON(t, anon, "POST", srv.URL+"/agent/v1/discovery/report", map[string]any{
		"token": token,
		"disks": []map[string]any{{"device": "sda", "sizeGiB": 40}},
	})
	_, discovered := doJSONList(t, client, srv.URL+"/api/v1/discovered-vms")
	dvID := discovered[0]["id"].(string)

	resp, body := doJSON(t, client, "POST", srv.URL+"/api/v1/discovered-vms/"+dvID+"/adopt",
		map[string]any{"mode": "new-deployment"})
	if resp.StatusCode != 409 || !strings.Contains(body["error"].(string), "identity") {
		t.Fatalf("adopt without identity: %d %v", resp.StatusCode, body)
	}

	// Dismissing removes it from the list.
	req, _ := http.NewRequest("DELETE", srv.URL+"/api/v1/discovered-vms/"+dvID, nil)
	if res, err := client.Do(req); err != nil || res.StatusCode != 204 {
		t.Fatalf("dismiss: %v %d", err, res.StatusCode)
	}
	_, discovered = doJSONList(t, client, srv.URL+"/api/v1/discovered-vms")
	if len(discovered) != 0 {
		t.Fatalf("dismissed VM still listed: %v", discovered)
	}
}

func TestDiscoveryDisabled(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secrets.Open(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	driver := fake.New()
	orchestrator := orch.New(st, box, hypervisor.Registry{"vsphere": driver.Factory})
	t.Cleanup(orchestrator.Stop)
	srv := httptest.NewServer(New(Config{
		Store: st, Secrets: box, WebDist: dir, Orch: orchestrator,
		// Discovery off: unknown MACs get the polite exit script.
	}))
	t.Cleanup(srv.Close)

	res, _ := http.Get(srv.URL + "/boot/script.ipxe?mac=de:ad:be:ef:00:01")
	body := new(bytes.Buffer)
	body.ReadFrom(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(body.String(), "not a managed VM") {
		t.Fatalf("unknown mac with discovery off: %d %s", res.StatusCode, body.String())
	}
	vms, err := st.ListDiscoveredVMs()
	if err != nil || len(vms) != 0 {
		t.Fatalf("no discovery record should be created: %v %v", err, vms)
	}
}
