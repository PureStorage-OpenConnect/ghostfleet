//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/datagen"
)

// TestPlanChurnBytes covers the byte target an incremental reports its
// progress against: the picked files' sizes plus the growth bytes.
func TestPlanChurnBytes(t *testing.T) {
	mnt := t.TempDir()
	dir := filepath.Join(mnt, "local")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	const fileSize = 1000
	for i := 0; i < 10; i++ {
		name := filepath.Join(dir, fmt.Sprintf("gf_local.0.%d", i))
		if err := os.WriteFile(name, make([]byte, fileSize), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	wo := &datagen.WorkOrder{ChangePercent: 20, GrowthPercent: 10, FileSizeBytes: 1 << 20}
	plan, err := planChurn(wo, mnt)
	if err != nil {
		t.Fatalf("planChurn: %v", err)
	}
	if len(plan.rewrite) != 2 {
		t.Fatalf("rewrite = %d file(s), want 2", len(plan.rewrite))
	}
	if plan.grow != 1000 { // 10% of 10 000 bytes of local data
		t.Fatalf("grow = %d, want 1000", plan.grow)
	}
	if got, want := plan.bytes(), int64(3000); got != want {
		t.Fatalf("bytes() = %d, want %d", got, want)
	}
}

// TestPlanChurnRoundsUpToOneFile keeps a tiny change% from planning zero work
// (and thus a zero byte target) when there is data to churn.
func TestPlanChurnRoundsUpToOneFile(t *testing.T) {
	mnt := t.TempDir()
	dir := filepath.Join(mnt, "local")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gf_local.0.0"), make([]byte, 512), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := planChurn(&datagen.WorkOrder{ChangePercent: 1, FileSizeBytes: 1 << 20}, mnt)
	if err != nil {
		t.Fatalf("planChurn: %v", err)
	}
	if len(plan.rewrite) != 1 || plan.bytes() != 512 {
		t.Fatalf("plan = %+v, want 1 file / 512 bytes", plan)
	}
}

// TestPlanChurnSpreadsChangeSet guards the churn bias that made verify runs
// fail: "gf_grow_*" sorts ahead of "gf_local.*", so taking the sorted prefix
// rewrote nothing but the oldest growth files, run after run, while the
// baseline data was never touched again.
func TestPlanChurnSpreadsChangeSet(t *testing.T) {
	mnt := t.TempDir()
	dir := filepath.Join(mnt, "local")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		writeChurnFile(t, dir, fmt.Sprintf("gf_grow_%08x.0.0", i), 1000)
	}
	for i := 0; i < 15; i++ {
		writeChurnFile(t, dir, fmt.Sprintf("gf_local.0.%d", i), 1000)
	}

	wo := &datagen.WorkOrder{ChangePercent: 25, FileSizeBytes: 1 << 20, RunSeed: 42}
	plan, err := planChurn(wo, mnt)
	if err != nil {
		t.Fatalf("planChurn: %v", err)
	}
	if len(plan.rewrite) != 5 {
		t.Fatalf("rewrite = %d file(s), want 5", len(plan.rewrite))
	}
	baseline := 0
	for _, f := range plan.rewrite {
		if strings.HasPrefix(f.name, "gf_local.") {
			baseline++
		}
	}
	if baseline == 0 {
		t.Fatalf("change set is all growth files (%+v) — the baseline is never churned", plan.rewrite)
	}
	// Name order is preserved, so fio job order and the console log stay stable.
	if !sort.SliceIsSorted(plan.rewrite, func(i, j int) bool { return plan.rewrite[i].name < plan.rewrite[j].name }) {
		t.Fatalf("change set is not name-sorted: %+v", plan.rewrite)
	}
}

// TestPickChangeSetSeeding keeps a re-dispatched run redoing exactly the same
// work (same seed → same pick) while successive runs churn different files.
func TestPickChangeSetSeeding(t *testing.T) {
	var files []planFile
	for i := 0; i < 20; i++ {
		files = append(files, planFile{name: fmt.Sprintf("gf_local.0.%02d", i), size: 1000})
	}
	names := func(p []planFile) string {
		var s []string
		for _, f := range p {
			s = append(s, f.name)
		}
		return strings.Join(s, ",")
	}
	a, b := names(pickChangeSet(files, 5, 7)), names(pickChangeSet(files, 5, 7))
	if a != b {
		t.Fatalf("same seed picked different files: %s vs %s", a, b)
	}
	if c := names(pickChangeSet(files, 5, 8)); c == a {
		t.Fatalf("a different run picked the same files (%s) — churn never moves on", c)
	}
	if got := pickChangeSet(files, 25, 7); len(got) != len(files) {
		t.Fatalf("n over the file count = %d file(s), want all %d", len(got), len(files))
	}
	if got := pickChangeSet(files, 0, 7); got != nil {
		t.Fatalf("n = 0 picked %d file(s), want none", len(got))
	}
}

func writeChurnFile(t *testing.T, dir, name string, size int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPlanChurnUnfilled reports a helpful error when the local region is
// missing (an incremental on a disk that was never filled).
func TestPlanChurnUnfilled(t *testing.T) {
	if _, err := planChurn(&datagen.WorkOrder{ChangePercent: 10}, t.TempDir()); err == nil {
		t.Fatal("planChurn on an unfilled disk: want error, got nil")
	}
}
