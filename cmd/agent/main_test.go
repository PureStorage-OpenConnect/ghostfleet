package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/datagen"
)

func newTestClient(base string) *httpClient {
	return &httpClient{
		base: base, token: "t",
		http:      &http.Client{Timeout: 5 * time.Second},
		completed: map[string]bool{},
	}
}

// A dropped terminal report must be retried until the controller accepts it
// (2xx), since the progress endpoint replies 204 — anything else means retry.
func TestPostReliableRetriesUntilAccepted(t *testing.T) {
	orig := terminalReportBackoff
	terminalReportBackoff = time.Millisecond
	defer func() { terminalReportBackoff = orig }()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent) // matches the real /agent/v1/progress
	}))
	defer srv.Close()

	newTestClient(srv.URL).reportDone(123)
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("expected 3 attempts (2 failures + success), got %d", got)
	}
}

func TestPostReliableGivesUp(t *testing.T) {
	orig := terminalReportBackoff
	terminalReportBackoff = time.Millisecond
	defer func() { terminalReportBackoff = orig }()

	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	newTestClient(srv.URL).reportError("boom")
	if got := atomic.LoadInt32(&calls); got != 12 {
		t.Fatalf("expected 12 attempts before giving up, got %d", got)
	}
}

// dispatch must not re-run a run already finished in this agent's lifetime,
// even if the controller keeps handing the same work order back.
func TestDispatchSkipsCompletedRun(t *testing.T) {
	c := newTestClient("http://unused")
	c.completed["run-1"] = true
	resp := &agentResponse{
		Action:    "fill",
		WorkOrder: &datagen.WorkOrder{RunID: "run-1"},
	}
	c.dispatch(resp) // must return immediately without touching c.running
	c.mu.Lock()
	running := c.running
	c.mu.Unlock()
	if running != "" {
		t.Fatalf("dispatch ran a completed run: running=%q", running)
	}
}
