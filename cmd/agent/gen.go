//go:build linux

package main

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/datagen"
)

// runFill dispatches a work order to the initial-fill, incremental or
// verify path.
func runFill(client *httpClient, wo *datagen.WorkOrder) {
	logf("run %s (%s): %d disk(s), compress=%d%% dedupe=%d%% rate=%dKB/s",
		wo.RunID, wo.RunType, len(wo.Disks), wo.CompressPercent, wo.DedupePercent, wo.RateLimitKBps)

	devices, err := dataDisks(len(wo.Disks))
	if err != nil {
		client.reportError(err.Error())
		return
	}
	mounts := make([]string, len(wo.Disks))
	for i, disk := range wo.Disks {
		mounts[i] = fmt.Sprintf("/mnt/disk%d", disk.Index)
	}

	// Verify is read-only: no completion marker is written (re-verifying is
	// always safe and always wanted), so no resume check either.
	if wo.RunType == "verify" {
		runVerify(client, wo, devices, mounts)
		return
	}

	// Resume: if every disk already carries this run's completion marker, the
	// run finished before (e.g. the VM rebooted, or a "done" report was lost) —
	// skip regenerating and just re-confirm. Re-running would be safe anyway
	// (deterministic seeds; growth files are named by run ID so fio overwrites
	// rather than double-appends), but this avoids a needless full re-fill.
	if total, ok := runAlreadyComplete(wo, devices, mounts); ok {
		logf("run %s already complete on disk — re-confirming (%d bytes)", wo.RunID, total)
		flushDisks(mounts) // the check mounted them read-write; leave them clean
		client.reportDone(total)
		return
	}

	if wo.RunType == "incremental" {
		runIncremental(client, wo, devices, mounts)
		return
	}
	runInitialFill(client, wo, devices, mounts)
}

// runMarkerFile records, at each disk's root, the run ID that last completed on
// it (plus the run's reported byte total) so a re-dispatched/rebooted agent can
// recognise an already-finished run.
const runMarkerFile = ".ghostfleet-run"

// runAlreadyComplete reports whether every disk already holds this run's
// completion marker, returning the recorded byte total to re-report. A disk
// with no filesystem (fresh initial fill) or a different/missing marker means
// "not complete".
func runAlreadyComplete(wo *datagen.WorkOrder, devices, mounts []string) (int64, bool) {
	var total int64
	for i := range wo.Disks {
		if err := mountFS(devices[i], mounts[i], wo.Filesystem); err != nil {
			return 0, false // no existing filesystem yet
		}
		data, err := os.ReadFile(filepath.Join(mounts[i], runMarkerFile))
		if err != nil {
			return 0, false
		}
		fields := strings.Fields(string(data))
		if len(fields) == 0 || fields[0] != wo.RunID {
			return 0, false
		}
		if len(fields) >= 2 {
			total, _ = strconv.ParseInt(fields[1], 10, 64)
		}
	}
	return total, true
}

// writeRunMarker records a completed run on a disk (best-effort).
func writeRunMarker(mnt, runID string, total int64) {
	if err := writeFileSync(filepath.Join(mnt, runMarkerFile),
		[]byte(fmt.Sprintf("%s %d", runID, total)), 0o644); err != nil {
		logf("writing run marker on %s: %v", mnt, err)
	}
}

// flushDisks ends a run by making every disk durable and clean: a global sync,
// then an unmount per disk. The unmount earns its place twice over — it
// flushes the filesystem, and it leaves the log clean, so a restored copy is
// readable without a journal replay. A disk that is not mounted (an early
// failure) is skipped quietly. Nothing after this point needs the mounts; the
// next run remounts from scratch.
func flushDisks(mounts []string) {
	syscall.Sync()
	for _, mnt := range mounts {
		err := syscall.Unmount(mnt, 0)
		if err != nil && err != syscall.EINVAL && err != syscall.ENOENT {
			logf("unmount %s: %v (data synced anyway)", mnt, err)
		}
	}
}

// runInitialFill makes a fresh filesystem per disk and fills it: a "global"
// region (deployment seed → cross-VM dedup) and a "local" region (per-VM
// seed → unique). Progress comes from fio's own reported bandwidth/percent.
func runInitialFill(client *httpClient, wo *datagen.WorkOrder, devices, mounts []string) {
	var total int64
	for _, d := range wo.Disks {
		total += d.GlobalBytes + d.LocalBytes
	}
	var prog progress
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); reportLoop(client, &prog, total, stop) }()

	runErr := func() error {
		for i, disk := range wo.Disks {
			dev, mnt := devices[i], mounts[i]
			if err := makeFS(dev, mnt, wo.Filesystem); err != nil {
				return fmt.Errorf("disk %s: %w", dev, err)
			}
			if err := fillRegion(wo, mnt, "global", disk.GlobalBytes, disk.GlobalSeed, &prog); err != nil {
				return fmt.Errorf("disk %s global: %w", dev, err)
			}
			if err := fillRegion(wo, mnt, "local", disk.LocalBytes, disk.LocalSeed, &prog); err != nil {
				return fmt.Errorf("disk %s local: %w", dev, err)
			}
			// Stamp identity and checksum manifest: the disk becomes
			// self-describing (adoptable after a restore) and verifiable.
			writeIdentity(mnt, wo, disk.Index)
			if err := updateDiskManifest(mnt, wo.RunID, nil); err != nil {
				return fmt.Errorf("disk %s manifest: %w", dev, err)
			}
		}
		return nil
	}()

	close(stop)
	wg.Wait()
	if runErr != nil {
		logf("fill failed: %v", runErr)
		flushDisks(mounts) // keep whatever did land, for the next attempt to resume from
		client.reportError(runErr.Error())
		return
	}
	for _, mnt := range mounts {
		writeRunMarker(mnt, wo.RunID, total)
	}
	flushDisks(mounts)
	logf("fill complete: %d bytes", total)
	client.reportDone(total)
}

// runIncremental mounts each disk's existing filesystem (no mkfs) and applies
// per-run churn to the local region: rewrites change% of existing files with
// fresh run-seeded content (CBT-visible) and appends growth% new files. The
// global cross-VM-dedup region is left as a stable baseline.
func runIncremental(client *httpClient, wo *datagen.WorkOrder, devices, mounts []string) {
	fail := func(err error) {
		logf("incremental failed: %v", err)
		client.reportError(err.Error())
	}

	// Plan every disk first (read-only: mount, list files, pick the change
	// set). The churn size depends on the data already on the disks, so this is
	// the only way to know the run's byte target before writing — without it
	// progress reports against a 0 total.
	plans := make([]churnPlan, len(wo.Disks))
	var total int64
	for i := range wo.Disks {
		dev, mnt := devices[i], mounts[i]
		if err := mountFS(dev, mnt, wo.Filesystem); err != nil {
			fail(fmt.Errorf("disk %s: %w", dev, err))
			return
		}
		p, err := planChurn(wo, mnt)
		if err != nil {
			fail(fmt.Errorf("disk %s: %w", dev, err))
			return
		}
		plans[i] = p
		total += p.bytes()
	}
	logf("incremental plan: %.1f GiB (%d file(s) rewritten, %.1f GiB grown) across %d disk(s)",
		float64(total)/(1<<30), rewriteCount(plans), float64(growBytes(plans))/(1<<30), len(plans))

	var prog progress
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); reportLoop(client, &prog, total, stop) }()

	runErr := func() error {
		for i, disk := range wo.Disks {
			mnt := mounts[i]
			changed, err := applyChurn(wo, plans[i], &prog)
			if err != nil {
				return fmt.Errorf("disk %s: %w", devices[i], err)
			}
			// Re-stamp identity (adoption may have moved the VM to a new
			// deployment) and fold the churn into the checksum manifest:
			// rewritten files are re-hashed, fresh growth files (absent from
			// the old manifest) are hashed, the rest keeps its checksums.
			writeIdentity(mnt, wo, disk.Index)
			if err := updateDiskManifest(mnt, wo.RunID, changed); err != nil {
				return fmt.Errorf("disk %s manifest: %w", devices[i], err)
			}
		}
		return nil
	}()

	close(stop)
	wg.Wait()
	if runErr != nil {
		flushDisks(mounts)
		fail(runErr)
		return
	}
	written := prog.bytes()
	for _, mnt := range mounts {
		writeRunMarker(mnt, wo.RunID, written)
	}
	flushDisks(mounts)
	logf("incremental complete: %d bytes written", written)
	client.reportDone(written)
}

// dataDisks returns the first n block devices (sorted), which on an OS-less
// ghost VM are exactly the data disks (no OS disk exists).
func dataDisks(n int) ([]string, error) {
	entries, err := os.ReadDir("/sys/block")
	if err != nil {
		return nil, fmt.Errorf("listing block devices: %w", err)
	}
	var disks []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "sd") || strings.HasPrefix(name, "vd") {
			disks = append(disks, name)
		}
	}
	sort.Strings(disks)
	if len(disks) < n {
		return nil, fmt.Errorf("need %d data disks, found %d (%v)", n, len(disks), disks)
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = "/dev/" + disks[i]
	}
	return out, nil
}

// makeFS creates a whole-disk filesystem and mounts it. (Whole-disk XFS
// keeps the minimal initramfs simple — no udev/partprobe; a GPT partition
// layer can be added later if a backup product needs the partition table.)
func makeFS(dev, mnt, fs string) error {
	// Re-runs (or a re-used agent) may have this target still mounted; a
	// fresh initial-fill starts from a clean filesystem.
	_ = syscall.Unmount(mnt, syscall.MNT_DETACH)

	logf("mkfs.%s %s", fs, dev)
	cmd := exec.Command("mkfs."+fs, "-q", "-f", dev)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mkfs: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return err
	}
	if err := syscall.Mount(dev, mnt, fs, 0, ""); err != nil {
		return fmt.Errorf("mount %s: %w", dev, err)
	}
	return nil
}

// mountFS mounts an existing filesystem without wiping it (incremental runs).
func mountFS(dev, mnt, fs string) error {
	_ = syscall.Unmount(mnt, syscall.MNT_DETACH)
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return err
	}
	if err := syscall.Mount(dev, mnt, fs, 0, ""); err != nil {
		return fmt.Errorf("mount existing %s: %w", dev, err)
	}
	return nil
}

// fillRegion writes `bytes` of data into mnt/region as `nrFiles` files via one
// fio job, so the whole region shares the given seed (reproducible, and
// cross-VM-identical for the global region).
func fillRegion(wo *datagen.WorkOrder, mnt, region string, bytes int64, seed uint64, prog *progress) error {
	if bytes <= 0 {
		return nil
	}
	dir := filepath.Join(mnt, region)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	nrFiles := (bytes + wo.FileSizeBytes - 1) / wo.FileSizeBytes
	logf("fio %s: %d bytes in %d file(s) seed=%d", region, bytes, nrFiles, seed)
	return runFioStreaming(wo, "gf_"+region, seed, bytes, []string{
		"--directory=" + dir,
		fmt.Sprintf("--size=%d", bytes),
		fmt.Sprintf("--nrfiles=%d", nrFiles),
		fmt.Sprintf("--filesize=%d", wo.FileSizeBytes),
	}, prog)
}

// churnPlan is one disk's planned incremental work, worked out before any
// writing starts so the run knows its byte target: the existing local files
// to rewrite in place (with fresh run-seeded content) and the bytes to append
// as new growth files.
type churnPlan struct {
	dir     string     // <mnt>/local
	rewrite []planFile // existing files to rewrite, in fio job order
	grow    int64      // bytes to append in new files
}

// planFile is an existing file and the size it will be rewritten at.
type planFile struct {
	name string
	size int64
}

// bytes is what applying the plan writes in total.
func (p churnPlan) bytes() int64 {
	total := p.grow
	for _, f := range p.rewrite {
		total += f.size
	}
	return total
}

func rewriteCount(plans []churnPlan) int {
	n := 0
	for _, p := range plans {
		n += len(p.rewrite)
	}
	return n
}

func growBytes(plans []churnPlan) int64 {
	var n int64
	for _, p := range plans {
		n += p.grow
	}
	return n
}

// planChurn works out one disk's incremental churn without writing anything:
// change% of the existing local files get rewritten (a deterministic subset
// spread across the region, at least one file if change>0) and growth% of the
// current local data is appended as new files.
func planChurn(wo *datagen.WorkOrder, mnt string) (churnPlan, error) {
	dir := filepath.Join(mnt, "local")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return churnPlan{}, fmt.Errorf("reading %s (was it filled?): %w", dir, err)
	}
	var files []planFile
	var localBytes int64
	for _, e := range entries {
		if e.Type().IsRegular() {
			info, err := e.Info()
			if err != nil {
				return churnPlan{}, err
			}
			files = append(files, planFile{name: e.Name(), size: info.Size()})
			localBytes += info.Size()
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })

	nChange := int(float64(len(files))*wo.ChangePercent/100.0 + 0.5)
	if wo.ChangePercent > 0 && nChange == 0 && len(files) > 0 {
		nChange = 1
	}
	if nChange > len(files) {
		nChange = len(files)
	}
	return churnPlan{
		dir:     dir,
		rewrite: pickChangeSet(files, nChange, wo.RunSeed),
		grow:    int64(float64(localBytes) * wo.GrowthPercent / 100.0),
	}, nil
}

// pickChangeSet chooses n files to rewrite, spread across the whole region
// rather than taken from the front of the sorted list: "gf_grow_*" sorts ahead
// of the "gf_local.*" baseline, so a prefix would rewrite the same handful of
// growth files every run and never touch the bulk of the data. The pick is
// seeded by the run, so it stays reproducible — a re-dispatched run redoes
// exactly the same work — while successive runs churn different files. The
// result keeps name order, so fio job order and the console log stay stable.
func pickChangeSet(files []planFile, n int, seed uint64) []planFile {
	if n <= 0 {
		return nil
	}
	if n >= len(files) {
		return files
	}
	idx := make([]int, len(files))
	for i := range idx {
		idx[i] = i
	}
	rng := rand.New(rand.NewSource(int64(seed)))
	rng.Shuffle(len(idx), func(i, j int) { idx[i], idx[j] = idx[j], idx[i] })
	idx = idx[:n]
	sort.Ints(idx)
	out := make([]planFile, 0, n)
	for _, i := range idx {
		out = append(out, files[i])
	}
	return out
}

// applyChurn executes one disk's plan: rewrite the picked files, then append
// the growth files. Progress is tracked via prog. Returns the touched files
// (disk-root-relative) so the manifest update re-hashes exactly those.
func applyChurn(wo *datagen.WorkOrder, plan churnPlan, prog *progress) (map[string]bool, error) {
	changed := make(map[string]bool)
	for i, f := range plan.rewrite {
		logf("incremental: rewrite %s (%d bytes)", f.name, f.size)
		if err := runFioStreaming(wo, "gf_chg", wo.RunSeed+uint64(i), f.size, []string{
			"--filename=" + filepath.Join(plan.dir, f.name),
			fmt.Sprintf("--size=%d", f.size),
		}, prog); err != nil {
			return nil, err
		}
		changed["local/"+f.name] = true
	}

	if plan.grow > 0 {
		nNew := (plan.grow + wo.FileSizeBytes - 1) / wo.FileSizeBytes
		logf("incremental: grow %d bytes in %d new file(s)", plan.grow, nNew)
		if err := runFioStreaming(wo, fmt.Sprintf("gf_grow_%s", shortID(wo.RunID)), wo.RunSeed^0x9e37, plan.grow, []string{
			"--directory=" + plan.dir,
			fmt.Sprintf("--size=%d", plan.grow),
			fmt.Sprintf("--nrfiles=%d", nNew),
			fmt.Sprintf("--filesize=%d", wo.FileSizeBytes),
		}, prog); err != nil {
			return nil, err
		}
		// A re-run of the same incremental overwrites its own growth files;
		// they are already in the manifest then, so flag them for re-hashing
		// (fresh growth files aren't in the manifest and are hashed anyway).
		for _, name := range growthFileNames(plan.dir, shortID(wo.RunID)) {
			changed["local/"+name] = true
		}
	}
	return changed, nil
}

// growthFileNames lists the files a growth job of the given run wrote
// (fio names them "gf_grow_<run>.<job>.<file>").
func growthFileNames(dir, runShort string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	prefix := fmt.Sprintf("gf_grow_%s.", runShort)
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasPrefix(e.Name(), prefix) {
			out = append(out, e.Name())
		}
	}
	return out
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// reportLoop posts a throughput sample to the controller every few seconds
// and logs a human progress line to the console (percent done when total>0,
// current and average MB/s), reading bytes/speed from fio-derived progress.
func reportLoop(client *httpClient, prog *progress, total int64, stop <-chan struct{}) {
	const interval = 5 * time.Second
	start := time.Now()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
			now := prog.bytes()
			cur := prog.curMBps()
			elapsed := time.Since(start).Seconds()
			avg := 0.0
			if elapsed > 0 {
				avg = float64(now) / 1e6 / elapsed
			}
			pct := ""
			if total > 0 {
				pct = fmt.Sprintf("%.0f%% ", float64(now)/float64(total)*100)
			}
			logf("progress: %s%.1f/%.1f GiB  cur %.0f MB/s  avg %.0f MB/s",
				pct, float64(now)/(1<<30), float64(total)/(1<<30), cur, avg)
			client.reportProgress(now, total, cur)
		}
	}
}

// fioError extracts the meaningful complaint from fio's output. On a bad
// invocation fio prints its error then dumps the full usage; we want the
// error line(s), not the usage footer.
func fioError(out string) string {
	var keep []string
	for _, ln := range strings.Split(out, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "--") || strings.HasPrefix(ln, "fio [options]") ||
			strings.HasPrefix(ln, "Fio was written") || strings.HasPrefix(ln, "fio-") {
			continue // skip usage/version noise
		}
		keep = append(keep, ln)
		if len(keep) == 3 {
			break
		}
	}
	if len(keep) == 0 {
		return "fio failed (no message)"
	}
	return strings.Join(keep, " | ")
}
