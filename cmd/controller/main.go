// The controller is the management brain of GhostFleet: it serves the web UI
// and REST API, talks to hypervisors through plugins, and orchestrates the
// data-generation agents. See docs/ARCHITECTURE.md.
package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	// Embed the IANA zone database so daily schedules can name a zone
	// ("Europe/Berlin") even on runtime images without /usr/share/zoneinfo.
	_ "time/tzdata"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/api"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/buildinfo"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/hypervisor"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/hypervisor/vsphere"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/orch"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/sched"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/secrets"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/store"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address for the web UI and API")
	webDist := flag.String("web", "web/dist", "directory containing the built web UI")
	dataDir := flag.String("data", "data", "directory for database and key material")
	imagesDir := flag.String("images", "images", "directory with temp-OS boot artifacts (vmlinuz, initramfs.gz)")
	bootURL := flag.String("boot-url", "http://[fd47:486f:7374::1]:8080",
		"controller base URL as reachable from the isolated network (used in iPXE scripts)")
	flag.Parse()

	if err := os.MkdirAll(*dataDir, 0o700); err != nil {
		slog.Error("creating data directory", "dir", *dataDir, "err", err)
		os.Exit(1)
	}
	box, err := secrets.Open(filepath.Join(*dataDir, "secret.key"))
	if err != nil {
		slog.Error("opening secret key", "err", err)
		os.Exit(1)
	}
	st, err := store.Open(filepath.Join(*dataDir, "ghostfleet.db"))
	if err != nil {
		slog.Error("opening database", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// Optional UI/API password (UI-1); empty means open access.
	password := os.Getenv("GHOSTFLEET_PASSWORD")
	// Optional static API keys for automation (comma-separated), accepted in
	// addition to the session cookie. Configuring any also turns the gate on.
	apiKeys := splitKeys(os.Getenv("GHOSTFLEET_API_KEYS"))
	// Discovery boots unknown MACs on the isolated network (e.g. backup
	// restores of GhostFleet VMs) into the temp OS for inspection/adoption.
	// On by default — discovery tokens are report-only, so the §7 boot-token
	// invariant holds; set GHOSTFLEET_DISCOVERY=off to disable.
	discovery := !strings.EqualFold(os.Getenv("GHOSTFLEET_DISCOVERY"), "off")

	slog.Info("controller starting",
		"version", buildinfo.Version, "addr", *addr,
		"data", *dataDir, "auth", password != "" || len(apiKeys) > 0,
		"apiKeys", len(apiKeys), "discovery", discovery)

	registry := hypervisor.Registry{"vsphere": vsphere.New}
	orchestrator := orch.New(st, box, registry)
	// Re-attach any jobs interrupted by a previous restart (NFR-2/NFR-3).
	orchestrator.ResumeInterrupted()

	// Serve in the background so we can shut down gracefully on a signal.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Fire schedules (timer-based runs); due times are persisted, so fires
	// missed while the controller was down are caught up once on start.
	go sched.New(st, orchestrator).Run(ctx)

	srv := &http.Server{Addr: *addr, Handler: api.New(api.Config{
		Store: st, Secrets: box, Orch: orchestrator,
		Password: password, APIKeys: apiKeys, WebDist: *webDist,
		ImagesDir: *imagesDir, BootURL: *bootURL, Discovery: discovery,
	})}

	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server exited", "err", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	slog.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("graceful shutdown", "err", err)
	}
}

// splitKeys parses a comma-separated key list, trimming blanks.
func splitKeys(s string) []string {
	var keys []string
	for _, k := range strings.Split(s, ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	return keys
}
