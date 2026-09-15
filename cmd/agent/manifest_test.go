package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/datagen"
	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

func writeTestFile(t *testing.T, mnt, rel, content string) {
	t.Helper()
	path := filepath.Join(mnt, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestManifestLifecycle(t *testing.T) {
	mnt := t.TempDir()
	writeTestFile(t, mnt, "global/gf_global.0.0", "global data")
	writeTestFile(t, mnt, "local/gf_local.0.0", "local data A")
	writeTestFile(t, mnt, "local/gf_local.0.1", "local data B")

	// Initial fill: everything is hashed.
	if err := updateDiskManifest(mnt, "run-1", nil); err != nil {
		t.Fatal(err)
	}
	m1, err := loadManifest(mnt)
	if err != nil {
		t.Fatal(err)
	}
	if m1.RunID != "run-1" || len(m1.Files) != 3 {
		t.Fatalf("manifest after fill: %+v", m1)
	}
	if m1.Files["local/gf_local.0.0"].Size != int64(len("local data A")) {
		t.Fatalf("size wrong: %+v", m1.Files["local/gf_local.0.0"])
	}

	// Incremental: one file rewritten (flagged), one growth file added, one
	// silently corrupted (NOT flagged — its stale hash must survive, that is
	// exactly what verify later catches).
	writeTestFile(t, mnt, "local/gf_local.0.0", "local data A changed")
	writeTestFile(t, mnt, "local/gf_grow_run2.0.0", "growth data")
	staleHash := m1.Files["local/gf_local.0.1"].SHA256
	writeTestFile(t, mnt, "local/gf_local.0.1", "corrupted!!")
	if err := updateDiskManifest(mnt, "run-2", map[string]bool{"local/gf_local.0.0": true}); err != nil {
		t.Fatal(err)
	}
	m2, err := loadManifest(mnt)
	if err != nil {
		t.Fatal(err)
	}
	if m2.RunID != "run-2" || len(m2.Files) != 4 {
		t.Fatalf("manifest after incremental: %+v", m2)
	}
	if m2.Files["local/gf_local.0.0"].SHA256 == m1.Files["local/gf_local.0.0"].SHA256 {
		t.Fatal("rewritten file was not re-hashed")
	}
	if _, ok := m2.Files["local/gf_grow_run2.0.0"]; !ok {
		t.Fatal("growth file missing from manifest")
	}
	if m2.Files["local/gf_local.0.1"].SHA256 != staleHash {
		t.Fatal("unflagged file was re-hashed (verify could never catch silent corruption)")
	}

	// A vanished file drops out of the manifest.
	os.Remove(filepath.Join(mnt, "local/gf_grow_run2.0.0"))
	if err := updateDiskManifest(mnt, "run-3", nil); err != nil {
		t.Fatal(err)
	}
	m3, _ := loadManifest(mnt)
	if len(m3.Files) != 3 {
		t.Fatalf("vanished file kept: %+v", m3.Files)
	}
}

// TestWriteFileSyncTruncates covers the rewrite case: markers are written
// repeatedly and a shorter value must not leave the previous one's tail
// behind (a run marker going from a long byte count to a short one).
func TestWriteFileSyncTruncates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker")
	if err := writeFileSync(path, []byte("run-1 123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileSync(path, []byte("run-2 1"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "run-2 1" {
		t.Fatalf("marker = %q, want %q", got, "run-2 1")
	}
}

func TestWriteIdentity(t *testing.T) {
	mnt := t.TempDir()
	wo := &datagen.WorkOrder{
		Identity: &datagen.IdentityMarker{
			DeploymentID: "dep-1", DeploymentName: "nightly", VMName: "gf-0001",
			Spec: model.ProfileSpec{DisksPerVM: 2, DiskSizeGiB: 100, DataPerDiskGiB: 50},
		},
	}
	writeIdentity(mnt, wo, 1)
	data, err := os.ReadFile(filepath.Join(mnt, datagen.IdentityFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"deploymentId":"dep-1"`, `"vmName":"gf-0001"`, `"diskIndex":1`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("identity marker missing %s: %s", want, data)
		}
	}

	// A work order from an older controller (no identity) writes nothing.
	mnt2 := t.TempDir()
	writeIdentity(mnt2, &datagen.WorkOrder{}, 0)
	if _, err := os.Stat(filepath.Join(mnt2, datagen.IdentityFile)); !os.IsNotExist(err) {
		t.Fatal("identity marker written without identity in the work order")
	}
}
