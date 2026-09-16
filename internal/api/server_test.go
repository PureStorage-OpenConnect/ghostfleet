package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/hypervisor"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/hypervisor/fake"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/orch"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/secrets"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/store"
)

func testServer(t *testing.T, password string) (*httptest.Server, *http.Client) {
	srv, client, _ := testServerWithDriver(t, password)
	return srv, client
}

// testServerWithDriver registers a fake driver under the "vsphere" plugin
// name so API tests exercise the full deploy path without a hypervisor.
func testServerWithDriver(t *testing.T, password string) (*httptest.Server, *http.Client, *fake.Driver) {
	t.Helper()
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
	srv := httptest.NewServer(New(Config{
		Store: st, Secrets: box, Orch: orchestrator, Password: password, WebDist: dir,
		Discovery: true, // the production default
	}))
	t.Cleanup(srv.Close)

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	return srv, client, driver
}

func doJSON(t *testing.T, client *http.Client, method, url string, body any) (*http.Response, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, url, &buf)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp, out
}

var specBody = map[string]any{
	"vmCount": 4, "namePrefix": "test", "disksPerVM": 2,
	"diskSizeGiB": 100, "dataPerDiskGiB": 50,
	"compressPercent": 50, "dedupePercent": 10, "crossVMDedupePercent": 10,
	"changePercent": 5, "growthPercent": 2,
}

func TestHealthz(t *testing.T) {
	srv, client := testServer(t, "")
	resp, body := doJSON(t, client, "GET", srv.URL+"/healthz", nil)
	if resp.StatusCode != 200 || body["status"] != "ok" {
		t.Fatalf("healthz: %d %v", resp.StatusCode, body)
	}
}

func TestAuthDisabledByDefault(t *testing.T) {
	srv, client := testServer(t, "")
	resp, _ := doJSON(t, client, "GET", srv.URL+"/api/v1/profiles", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("open instance should not require auth, got %d", resp.StatusCode)
	}
}

func TestAuthFlow(t *testing.T) {
	srv, client := testServer(t, "hunter2")

	resp, _ := doJSON(t, client, "GET", srv.URL+"/api/v1/profiles", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401 before login, got %d", resp.StatusCode)
	}

	resp, _ = doJSON(t, client, "POST", srv.URL+"/api/v1/session", map[string]string{"password": "wrong"})
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401 for wrong password, got %d", resp.StatusCode)
	}

	resp, _ = doJSON(t, client, "POST", srv.URL+"/api/v1/session", map[string]string{"password": "hunter2"})
	if resp.StatusCode != 200 {
		t.Fatalf("login failed: %d", resp.StatusCode)
	}
	resp, _ = doJSON(t, client, "GET", srv.URL+"/api/v1/profiles", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 after login, got %d", resp.StatusCode)
	}

	resp, _ = doJSON(t, client, "DELETE", srv.URL+"/api/v1/session", nil)
	if resp.StatusCode != 204 {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	resp, _ = doJSON(t, client, "GET", srv.URL+"/api/v1/profiles", nil)
	if resp.StatusCode != 401 {
		t.Fatalf("expected 401 after logout, got %d", resp.StatusCode)
	}
}

func TestProfileCRUDAndVersioning(t *testing.T) {
	srv, client := testServer(t, "")

	resp, p := doJSON(t, client, "POST", srv.URL+"/api/v1/profiles",
		map[string]any{"name": "alpha", "spec": specBody})
	if resp.StatusCode != 201 {
		t.Fatalf("create: %d %v", resp.StatusCode, p)
	}
	id := p["id"].(string)
	if p["currentVersion"].(float64) != 1 {
		t.Fatalf("currentVersion = %v, want 1", p["currentVersion"])
	}

	// Spec update bumps the version.
	spec2 := map[string]any{}
	for k, v := range specBody {
		spec2[k] = v
	}
	spec2["vmCount"] = 8
	resp, p = doJSON(t, client, "PUT", srv.URL+"/api/v1/profiles/"+id, map[string]any{"spec": spec2})
	if resp.StatusCode != 200 || p["currentVersion"].(float64) != 2 {
		t.Fatalf("update: %d %v", resp.StatusCode, p)
	}

	// Invalid spec is rejected with a useful message.
	bad := map[string]any{}
	for k, v := range specBody {
		bad[k] = v
	}
	bad["dataPerDiskGiB"] = 999
	resp, body := doJSON(t, client, "PUT", srv.URL+"/api/v1/profiles/"+id, map[string]any{"spec": bad})
	if resp.StatusCode != 400 || !strings.Contains(body["error"].(string), "dataPerDiskGiB") {
		t.Fatalf("invalid spec: %d %v", resp.StatusCode, body)
	}

	// Old version remains retrievable.
	resp, v1 := doJSON(t, client, "GET", srv.URL+"/api/v1/profiles/"+id+"/versions/1", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get v1: %d", resp.StatusCode)
	}
	if v1["spec"].(map[string]any)["vmCount"].(float64) != 4 {
		t.Fatalf("v1 spec mutated: %v", v1["spec"])
	}
}

func TestConnectionSecretNeverReturned(t *testing.T) {
	srv, client := testServer(t, "")

	resp, c := doJSON(t, client, "POST", srv.URL+"/api/v1/connections", map[string]any{
		"name": "vc", "plugin": "vsphere", "endpoint": "https://vc.lab",
		"username": "admin", "secret": "supersecret",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create connection: %d %v", resp.StatusCode, c)
	}
	raw, _ := json.Marshal(c)
	if bytes.Contains(raw, []byte("supersecret")) {
		t.Fatal("secret leaked in create response")
	}
	resp, c = doJSON(t, client, "GET", srv.URL+"/api/v1/connections/"+c["id"].(string), nil)
	raw, _ = json.Marshal(c)
	if resp.StatusCode != 200 || bytes.Contains(raw, []byte("supersecret")) {
		t.Fatalf("secret leaked in get response: %d %s", resp.StatusCode, raw)
	}
}

func TestDeploymentFromProfileAndShrinkGuard(t *testing.T) {
	srv, client := testServer(t, "")

	_, p := doJSON(t, client, "POST", srv.URL+"/api/v1/profiles",
		map[string]any{"name": "prof", "spec": specBody})
	_, c := doJSON(t, client, "POST", srv.URL+"/api/v1/connections", map[string]any{
		"name": "vc", "plugin": "vsphere", "endpoint": "https://vc.lab",
		"username": "admin", "secret": "x",
	})

	resp, d := doJSON(t, client, "POST", srv.URL+"/api/v1/deployments", map[string]any{
		"name": "deploy-1", "profileId": p["id"], "connectionId": c["id"],
		"placement": map[string]string{"cluster": "C1", "datastore": "DS1"},
	})
	if resp.StatusCode != 201 {
		t.Fatalf("create deployment: %d %v", resp.StatusCode, d)
	}
	if d["status"] != "new" || d["profileVersion"].(float64) != 1 {
		t.Fatalf("deployment: %v", d)
	}
	// Effective config was snapshotted from the profile.
	if d["spec"].(map[string]any)["vmCount"].(float64) != 4 {
		t.Fatalf("effective spec: %v", d["spec"])
	}

	// Growing is fine, shrinking is rejected (PRO-6).
	grown := map[string]any{}
	for k, v := range specBody {
		grown[k] = v
	}
	grown["vmCount"] = 6
	url := fmt.Sprintf("%s/api/v1/deployments/%s/spec", srv.URL, d["id"])
	resp, _ = doJSON(t, client, "PUT", url, map[string]any{"spec": grown})
	if resp.StatusCode != 200 {
		t.Fatalf("grow: %d", resp.StatusCode)
	}
	shrunk := map[string]any{}
	for k, v := range specBody {
		shrunk[k] = v
	}
	shrunk["vmCount"] = 2
	resp, body := doJSON(t, client, "PUT", url, map[string]any{"spec": shrunk})
	if resp.StatusCode != 409 || !strings.Contains(body["error"].(string), "shrink") {
		t.Fatalf("shrink guard: %d %v", resp.StatusCode, body)
	}

	// Delete keeps the record with status=deleted.
	resp, _ = doJSON(t, client, "DELETE", fmt.Sprintf("%s/api/v1/deployments/%s", srv.URL, d["id"]), nil)
	if resp.StatusCode != 204 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	resp, d = doJSON(t, client, "GET", fmt.Sprintf("%s/api/v1/deployments/%s", srv.URL, d["id"]), nil)
	if resp.StatusCode != 200 || d["status"] != "deleted" {
		t.Fatalf("after delete: %d %v", resp.StatusCode, d)
	}
}

func TestDeployFlowWithPlacementAndVMs(t *testing.T) {
	srv, client, driver := testServerWithDriver(t, "")

	_, p := doJSON(t, client, "POST", srv.URL+"/api/v1/profiles",
		map[string]any{"name": "prof", "spec": specBody})
	_, c := doJSON(t, client, "POST", srv.URL+"/api/v1/connections", map[string]any{
		"name": "vc", "plugin": "vsphere", "endpoint": "https://vc.lab",
		"username": "admin", "secret": "x",
	})
	cid := c["id"].(string)

	// Validate + placement use the driver.
	resp, info := doJSON(t, client, "POST", srv.URL+"/api/v1/connections/"+cid+"/validate", nil)
	if resp.StatusCode != 200 || info["product"] != "FakeVisor" {
		t.Fatalf("validate: %d %v", resp.StatusCode, info)
	}
	resp, placement := doJSON(t, client, "GET", srv.URL+"/api/v1/connections/"+cid+"/placement", nil)
	if resp.StatusCode != 200 || placement["clusters"] == nil {
		t.Fatalf("placement: %d %v", resp.StatusCode, placement)
	}

	_, d := doJSON(t, client, "POST", srv.URL+"/api/v1/deployments", map[string]any{
		"name": "dep-1", "profileId": p["id"], "connectionId": cid,
		"placement": map[string]string{"cluster": "C0", "datastore": "LocalDS_0"},
	})
	did := d["id"].(string)

	// Deploy and wait for ready.
	resp, run := doJSON(t, client, "POST", srv.URL+"/api/v1/deployments/"+did+"/deploy", nil)
	if resp.StatusCode != 202 || run["type"] != "deploy" {
		t.Fatalf("deploy: %d %v", resp.StatusCode, run)
	}
	waitStatus(t, client, srv.URL, did, "ready")
	if driver.VMCount() != 4 {
		t.Fatalf("driver has %d VMs, want 4", driver.VMCount())
	}

	// VM list reflects reality.
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/deployments/"+did+"/vms", nil)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var vms []map[string]any
	json.NewDecoder(res.Body).Decode(&vms)
	res.Body.Close()
	if len(vms) != 4 || vms[0]["name"] != "test-0001" {
		t.Fatalf("vms: %v", vms)
	}

	// Second deploy while idle is a no-op reconcile; busy conflict is only
	// transient, so just check teardown via DELETE.
	resp, run = doJSON(t, client, "DELETE", srv.URL+"/api/v1/deployments/"+did, nil)
	if resp.StatusCode != 202 || run["type"] != "teardown" {
		t.Fatalf("teardown: %d %v", resp.StatusCode, run)
	}
	waitStatus(t, client, srv.URL, did, "deleted")
	if driver.VMCount() != 0 {
		t.Fatalf("driver still has %d VMs", driver.VMCount())
	}
}

func TestIncrementalBeforeFillReturns409(t *testing.T) {
	srv, client, _ := testServerWithDriver(t, "")

	_, p := doJSON(t, client, "POST", srv.URL+"/api/v1/profiles",
		map[string]any{"name": "prof", "spec": specBody})
	_, c := doJSON(t, client, "POST", srv.URL+"/api/v1/connections", map[string]any{
		"name": "vc", "plugin": "vsphere", "endpoint": "https://vc.lab",
		"username": "admin", "secret": "x",
	})
	_, d := doJSON(t, client, "POST", srv.URL+"/api/v1/deployments", map[string]any{
		"name": "dep-1", "profileId": p["id"], "connectionId": c["id"],
		"placement": map[string]string{"cluster": "C0", "datastore": "LocalDS_0"},
	})
	did := d["id"].(string)
	doJSON(t, client, "POST", srv.URL+"/api/v1/deployments/"+did+"/deploy", nil)
	waitStatus(t, client, srv.URL, did, "ready")

	// filled is false until an initial fill has succeeded (gates the UI button).
	_, dd := doJSON(t, client, "GET", srv.URL+"/api/v1/deployments/"+did, nil)
	if dd["filled"] != false {
		t.Fatalf("filled should be false before any fill, got %v", dd["filled"])
	}

	// Incremental before any initial fill must be a 409 with an explanatory
	// message — not the generic 500 it used to return.
	resp, body := doJSON(t, client, "POST", srv.URL+"/api/v1/deployments/"+did+"/fill",
		map[string]any{"type": "incremental"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("incremental before fill: status %d, want 409 (body %v)", resp.StatusCode, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "initial fill") {
		t.Fatalf("expected an explanatory error mentioning the initial fill, got %q", msg)
	}
}

// TestCancelNoRunningJob covers the cancel endpoint when nothing is running.
func TestCancelNoRunningJob(t *testing.T) {
	srv, client, _ := testServerWithDriver(t, "")
	_, p := doJSON(t, client, "POST", srv.URL+"/api/v1/profiles",
		map[string]any{"name": "prof", "spec": specBody})
	_, c := doJSON(t, client, "POST", srv.URL+"/api/v1/connections", map[string]any{
		"name": "vc", "plugin": "vsphere", "endpoint": "https://vc.lab",
		"username": "admin", "secret": "x",
	})
	_, d := doJSON(t, client, "POST", srv.URL+"/api/v1/deployments", map[string]any{
		"name": "dep-1", "profileId": p["id"], "connectionId": c["id"],
		"placement": map[string]string{"cluster": "C0", "datastore": "LocalDS_0"},
	})
	resp, _ := doJSON(t, client, "POST", srv.URL+"/api/v1/deployments/"+d["id"].(string)+"/cancel", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("cancel with no running job: status %d, want 409", resp.StatusCode)
	}
}

// TestSessionCookieSecure covers the Secure attribute policy: off on plain
// HTTP (the default deployment), on behind a TLS-terminating proxy, and
// forced either way by Config.SecureCookies.
func TestSessionCookieSecure(t *testing.T) {
	login := func(t *testing.T, secureCookies string, hdr http.Header) []*http.Cookie {
		t.Helper()
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
		h := New(Config{Store: st, Secrets: box, Password: "hunter2", SecureCookies: secureCookies, WebDist: dir})
		req := httptest.NewRequest("POST", "/api/v1/session", strings.NewReader(`{"password":"hunter2"}`))
		for k, v := range hdr {
			req.Header[k] = v
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("login: %d %s", rec.Code, rec.Body)
		}
		cookies := rec.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Name != sessionCookie {
			t.Fatalf("cookies = %v", cookies)
		}
		return cookies
	}
	cases := []struct {
		name   string
		mode   string
		hdr    http.Header
		secure bool
	}{
		{"plain http auto", "", nil, false},
		{"proxy https auto", "", http.Header{"X-Forwarded-Proto": {"https"}}, true},
		{"proxy chain auto", "", http.Header{"X-Forwarded-Proto": {"https, http"}}, true},
		{"proxy http auto", "", http.Header{"X-Forwarded-Proto": {"http"}}, false},
		{"forced on", "on", nil, true},
		{"forced off", "off", http.Header{"X-Forwarded-Proto": {"https"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := login(t, tc.mode, tc.hdr)[0]
			if c.Secure != tc.secure {
				t.Fatalf("Secure = %v, want %v", c.Secure, tc.secure)
			}
			if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
				t.Fatalf("HttpOnly=%v SameSite=%v", c.HttpOnly, c.SameSite)
			}
		})
	}
}

func TestAPIKeyAuth(t *testing.T) {
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
	orchestrator := orch.New(st, box, hypervisor.Registry{})
	srv := httptest.NewServer(New(Config{
		Store: st, Secrets: box, Orch: orchestrator, APIKeys: []string{"good-key"}, WebDist: dir,
	}))
	t.Cleanup(srv.Close)
	client := &http.Client{}

	get := func(setup func(*http.Request)) int {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/profiles", nil)
		if setup != nil {
			setup(req)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}

	cases := []struct {
		name  string
		setup func(*http.Request)
		want  int
	}{
		{"no credential", nil, http.StatusUnauthorized},
		{"wrong key header", func(r *http.Request) { r.Header.Set("X-API-Key", "nope") }, http.StatusUnauthorized},
		{"X-API-Key header", func(r *http.Request) { r.Header.Set("X-API-Key", "good-key") }, http.StatusOK},
		{"Bearer header", func(r *http.Request) { r.Header.Set("Authorization", "Bearer good-key") }, http.StatusOK},
		{"query param", func(r *http.Request) { r.URL.RawQuery = "apikey=good-key" }, http.StatusOK},
	}
	for _, c := range cases {
		if got := get(c.setup); got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}

	// session status reports authenticated when a valid key is presented.
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/session", nil)
	req.Header.Set("X-API-Key", "good-key")
	res, _ := client.Do(req)
	var body map[string]bool
	json.NewDecoder(res.Body).Decode(&body)
	res.Body.Close()
	if !body["authenticated"] {
		t.Errorf("session status: authenticated=false with a valid key")
	}
}

func TestProfileExportImport(t *testing.T) {
	srv, client := testServer(t, "")

	_, p := doJSON(t, client, "POST", srv.URL+"/api/v1/profiles",
		map[string]any{"name": "orig", "spec": specBody})
	id := p["id"].(string)

	// Export: attachment + a self-describing document.
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/profiles/"+id+"/export", nil)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 || !strings.Contains(res.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("export: %d %q", res.StatusCode, res.Header.Get("Content-Disposition"))
	}
	var doc map[string]any
	json.NewDecoder(res.Body).Decode(&doc)
	res.Body.Close()
	if doc["kind"] != "ghostfleet.profile" || doc["name"] != "orig" || doc["spec"] == nil {
		t.Fatalf("export doc: %v", doc)
	}

	// Import under a new name round-trips into a fresh profile at version 1.
	doc["name"] = "copied"
	resp, body := doJSON(t, client, "POST", srv.URL+"/api/v1/profiles/import", doc)
	if resp.StatusCode != 201 {
		t.Fatalf("import: %d %v", resp.StatusCode, body)
	}
	if body["name"] != "copied" || body["currentVersion"] != float64(1) {
		t.Fatalf("imported profile: %v", body)
	}
	if spec, _ := body["spec"].(map[string]any); spec["vmCount"] != float64(4) {
		t.Fatalf("imported spec not preserved: %v", body["spec"])
	}

	// Re-importing onto an existing name is a 409, not a silent overwrite.
	resp, _ = doJSON(t, client, "POST", srv.URL+"/api/v1/profiles/import", doc)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate import: %d, want 409", resp.StatusCode)
	}
}

func TestDeployConflictEndpoint(t *testing.T) {
	srv, client, driver := testServerWithDriver(t, "")
	_, p := doJSON(t, client, "POST", srv.URL+"/api/v1/profiles",
		map[string]any{"name": "prof", "spec": specBody})
	_, c := doJSON(t, client, "POST", srv.URL+"/api/v1/connections", map[string]any{
		"name": "vc", "plugin": "vsphere", "endpoint": "https://vc.lab", "username": "admin", "secret": "x",
	})
	_, d := doJSON(t, client, "POST", srv.URL+"/api/v1/deployments", map[string]any{
		"name": "dep-1", "profileId": p["id"], "connectionId": c["id"],
		"placement": map[string]string{"cluster": "C0", "datastore": "LocalDS_0"},
	})
	did := d["id"].(string)

	conflicts := func() []map[string]any {
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/deployments/"+did+"/conflicts", nil)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("conflicts status %d", res.StatusCode)
		}
		var out []map[string]any
		json.NewDecoder(res.Body).Decode(&out)
		return out
	}

	if got := conflicts(); len(got) != 0 {
		t.Fatalf("fresh deployment should have no conflicts, got %v", got)
	}

	// A foreign VM occupying test-0001 is reported as a conflict.
	driver.CreateVM(context.Background(), hypervisor.VMSpec{Name: "test-0001", DeploymentID: "other-dep"})
	got := conflicts()
	if len(got) != 1 || got[0]["name"] != "test-0001" || got[0]["ownerId"] != "other-dep" {
		t.Fatalf("expected one conflict for test-0001/other-dep, got %v", got)
	}
}

func waitStatus(t *testing.T, client *http.Client, base, id, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, d := doJSON(t, client, "GET", base+"/api/v1/deployments/"+id, nil)
		if d["status"] == want {
			return
		}
		if d["status"] == "error" && want != "error" {
			t.Fatalf("deployment errored: %v", d)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("deployment never reached %q", want)
}

func TestBootAndAgentFlow(t *testing.T) {
	srv, client, driver := testServerWithDriver(t, "ignored-for-boot-plane")

	// Session auth must NOT gate the boot/agent plane, so log in only for
	// the management calls.
	doJSON(t, client, "POST", srv.URL+"/api/v1/session", map[string]string{"password": "ignored-for-boot-plane"})

	_, p := doJSON(t, client, "POST", srv.URL+"/api/v1/profiles",
		map[string]any{"name": "boot-prof", "spec": specBody})
	_, c := doJSON(t, client, "POST", srv.URL+"/api/v1/connections", map[string]any{
		"name": "vc", "plugin": "vsphere", "endpoint": "https://vc.lab",
		"username": "admin", "secret": "x",
	})
	_, d := doJSON(t, client, "POST", srv.URL+"/api/v1/deployments", map[string]any{
		"name": "boot-dep", "profileId": p["id"], "connectionId": c["id"],
		"placement": map[string]string{"cluster": "C0", "datastore": "LocalDS_0"},
	})
	did := d["id"].(string)
	doJSON(t, client, "POST", srv.URL+"/api/v1/deployments/"+did+"/deploy", nil)
	waitStatus(t, client, srv.URL, did, "ready")
	if driver.VMCount() != 4 {
		t.Fatalf("vm count %d", driver.VMCount())
	}

	// The VM list exposes MACs but never boot tokens.
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/deployments/"+did+"/vms", nil)
	res, _ := client.Do(req)
	raw := new(bytes.Buffer)
	raw.ReadFrom(res.Body)
	res.Body.Close()
	var vms []map[string]any
	json.Unmarshal(raw.Bytes(), &vms)
	mac := vms[0]["mac"].(string)
	if mac == "" {
		t.Fatal("VM has no MAC recorded")
	}
	if bytes.Contains(raw.Bytes(), []byte("bootToken")) || bytes.Contains(raw.Bytes(), []byte("boot_token")) {
		t.Fatal("boot token leaked through management API")
	}

	// Anonymous boot-plane client (no session cookie).
	anon := &http.Client{}

	// iPXE script by MAC carries kernel/initrd/token.
	res, err := anon.Get(srv.URL + "/boot/script.ipxe?mac=" + mac)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("boot script: %v %d", err, res.StatusCode)
	}
	script := new(bytes.Buffer)
	script.ReadFrom(res.Body)
	res.Body.Close()
	if !strings.Contains(script.String(), "ghostfleet.token=") ||
		!strings.Contains(script.String(), "/boot/vmlinuz") {
		t.Fatalf("script incomplete: %s", script.String())
	}
	token := ""
	for _, f := range strings.Fields(script.String()) {
		if strings.HasPrefix(f, "ghostfleet.token=") {
			token = strings.TrimPrefix(f, "ghostfleet.token=")
		}
	}

	// Unknown MAC gets a discovery boot (with a token distinct from any
	// managed VM's boot token), not an error.
	res, _ = anon.Get(srv.URL + "/boot/script.ipxe?mac=de:ad:be:ef:00:01")
	body := new(bytes.Buffer)
	body.ReadFrom(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || !strings.Contains(body.String(), "ghostfleet.discover=1") {
		t.Fatalf("unknown mac: %d %s", res.StatusCode, body.String())
	}
	if strings.Contains(body.String(), token) {
		t.Fatal("discovery script must not carry a managed VM's boot token")
	}

	// Agent register + heartbeat with the token (no session).
	resp, reg := doJSON(t, anon, "POST", srv.URL+"/agent/v1/register", map[string]string{"token": token})
	if resp.StatusCode != 200 || reg["action"] != "idle" {
		t.Fatalf("register: %d %v", resp.StatusCode, reg)
	}
	resp, hb := doJSON(t, anon, "POST", srv.URL+"/agent/v1/heartbeat", map[string]string{"token": token})
	if resp.StatusCode != 200 || hb["action"] != "idle" {
		t.Fatalf("heartbeat: %d %v", resp.StatusCode, hb)
	}
	resp, _ = doJSON(t, anon, "POST", srv.URL+"/agent/v1/register", map[string]string{"token": "wrong"})
	if resp.StatusCode != 401 {
		t.Fatalf("bad token: %d", resp.StatusCode)
	}

	// Agent status visible via management API.
	_, vmsAfter := doJSONList(t, client, srv.URL+"/api/v1/deployments/"+did+"/vms")
	online := 0
	for _, vm := range vmsAfter {
		if vm["agentStatus"] == "online" {
			online++
		}
	}
	if online != 1 {
		t.Fatalf("online agents = %d, want 1", online)
	}

	// Power job flips all VMs on.
	resp, run := doJSON(t, client, "POST", srv.URL+"/api/v1/deployments/"+did+"/power", map[string]bool{"on": true})
	if resp.StatusCode != 202 || run["type"] != "power-on" {
		t.Fatalf("power: %d %v", resp.StatusCode, run)
	}
}

func doJSONList(t *testing.T, client *http.Client, url string) (*http.Response, []map[string]any) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out []map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res, out
}

func TestSplitConsoleSessions(t *testing.T) {
	// Two boots; each boot's kernel banner + RTC line precede its agent output.
	log := "[ 0.00] Linux version 6.12\n" +
		"[ 0.85] rtc_cmos: setting system clock to 2026-06-15T10:00:00 UTC (1)\n" +
		"[ghostfleet-agent] v1 starting\n[ghostfleet-agent] fill complete\n" +
		"[ 0.00] Linux version 6.12\n" +
		"[ 0.85] rtc_cmos: setting system clock to 2026-06-15T12:30:00 UTC (2)\n" +
		"[ghostfleet-agent] v2 starting\n[ghostfleet-agent] progress: 50%\n"
	s := splitConsoleSessions(log)
	if len(s) != 2 {
		t.Fatalf("got %d sessions, want 2", len(s))
	}
	// Each boot's kernel banner stays with its own session (the bug fix).
	if !strings.Contains(s[0].Text, "fill complete") || strings.Contains(s[0].Text, "v2 starting") {
		t.Fatalf("session 1 boundary wrong:\n%s", s[0].Text)
	}
	if !strings.Contains(s[1].Text, "progress: 50%") || !strings.Contains(s[1].Text, "Linux version") {
		t.Fatalf("session 2 should start at its own kernel banner:\n%s", s[1].Text)
	}
	// Timestamps come from each boot's RTC line.
	if s[0].StartedAt != "2026-06-15T10:00:00" || s[1].StartedAt != "2026-06-15T12:30:00" {
		t.Fatalf("timestamps: %q, %q", s[0].StartedAt, s[1].StartedAt)
	}
	if s[0].Partial || s[1].Partial {
		t.Fatal("full sessions (with banner) should not be partial")
	}
	// A leading chunk with no banner (tail-cut older boot) is partial.
	p := splitConsoleSessions("agent progress tail...\n[ 0.00] Linux version 6.12\nnew boot\n")
	if len(p) != 2 || !p[0].Partial || p[1].Partial {
		t.Fatalf("partial detection wrong: %+v", p)
	}
	if len(splitConsoleSessions("")) != 0 {
		t.Fatal("empty log should yield no sessions")
	}
}

func TestScheduleCRUD(t *testing.T) {
	srv, client, _ := testServerWithDriver(t, "")

	_, p := doJSON(t, client, "POST", srv.URL+"/api/v1/profiles",
		map[string]any{"name": "prof", "spec": specBody})
	_, c := doJSON(t, client, "POST", srv.URL+"/api/v1/connections", map[string]any{
		"name": "vc", "plugin": "vsphere", "endpoint": "https://vc.lab",
		"username": "admin", "secret": "x",
	})
	_, d := doJSON(t, client, "POST", srv.URL+"/api/v1/deployments", map[string]any{
		"name": "dep-1", "profileId": p["id"], "connectionId": c["id"],
		"placement": map[string]string{"cluster": "C0", "datastore": "LocalDS_0"},
	})
	did := d["id"].(string)

	// Create: next fire time is computed server-side.
	resp, sc := doJSON(t, client, "POST", srv.URL+"/api/v1/deployments/"+did+"/schedules",
		map[string]any{"action": "incremental", "kind": "every", "spec": "24h"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create schedule: status %d (%v)", resp.StatusCode, sc)
	}
	if sc["nextRunAt"] == nil || sc["enabled"] != true {
		t.Fatalf("created schedule missing nextRunAt/enabled: %v", sc)
	}
	scid := sc["id"].(string)

	// Invalid specs are rejected with a helpful message.
	resp, body := doJSON(t, client, "POST", srv.URL+"/api/v1/deployments/"+did+"/schedules",
		map[string]any{"action": "incremental", "kind": "daily", "spec": "25:99"})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body["error"].(string), "daily") {
		t.Fatalf("bad daily spec: status %d, body %v", resp.StatusCode, body)
	}
	resp, body = doJSON(t, client, "POST", srv.URL+"/api/v1/deployments/"+did+"/schedules",
		map[string]any{"action": "teardown", "kind": "once", "spec": "2001-01-01T00:00:00Z"})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body["error"].(string), "future") {
		t.Fatalf("once in the past: status %d, body %v", resp.StatusCode, body)
	}

	// Update: pause (enabled=false) clears the next fire time.
	resp, sc = doJSON(t, client, "PUT", srv.URL+"/api/v1/schedules/"+scid,
		map[string]any{"action": "incremental", "kind": "every", "spec": "12h", "enabled": false})
	if resp.StatusCode != http.StatusOK || sc["enabled"] != false || sc["nextRunAt"] != nil {
		t.Fatalf("pause: status %d, body %v", resp.StatusCode, sc)
	}

	// List, then delete.
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/deployments/"+did+"/schedules", nil)
	lresp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var list []map[string]any
	json.NewDecoder(lresp.Body).Decode(&list)
	lresp.Body.Close()
	if len(list) != 1 || list[0]["id"] != scid {
		t.Fatalf("list schedules: %v", list)
	}
	resp, _ = doJSON(t, client, "DELETE", srv.URL+"/api/v1/schedules/"+scid, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete schedule: status %d", resp.StatusCode)
	}
	resp, _ = doJSON(t, client, "GET", srv.URL+"/api/v1/schedules/"+scid, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("get deleted schedule: status %d, want 404", resp.StatusCode)
	}
}
