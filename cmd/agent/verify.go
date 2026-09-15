//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/datagen"
)

// mountFSReadOnly mounts an existing filesystem read-only — verify and
// discovery must never modify the data they are judging. (XFS may still
// replay its journal onto the device if the filesystem is dirty, e.g. from a
// crash-consistent backup; that is recovery, not data modification.)
func mountFSReadOnly(dev, mnt, fs string) error {
	_ = syscall.Unmount(mnt, syscall.MNT_DETACH)
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return err
	}
	if err := syscall.Mount(dev, mnt, fs, syscall.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("mount %s read-only: %w", dev, err)
	}
	return nil
}

// runVerify reads every generated file back and checks it against the disk's
// manifest: sizes and SHA-256 must match, nothing may be missing, nothing
// unexpected may exist. This is how a backup restore is proven intact.
func runVerify(client *httpClient, wo *datagen.WorkOrder, devices, mounts []string) {
	type disk struct {
		mnt      string
		manifest *datagen.Manifest
		files    []string
	}
	var disks []disk
	var total int64
	for i := range wo.Disks {
		dev, mnt := devices[i], mounts[i]
		if err := mountFSReadOnly(dev, mnt, wo.Filesystem); err != nil {
			client.reportError(fmt.Sprintf("disk %s: %v (was it ever filled?)", dev, err))
			return
		}
		m, err := loadManifest(mnt)
		if err != nil {
			client.reportError(fmt.Sprintf(
				"disk %s: no checksum manifest (%v) — data predates verify support; run a fill/incremental first", dev, err))
			return
		}
		files, err := listDataFiles(mnt)
		if err != nil {
			client.reportError(fmt.Sprintf("disk %s: %v", dev, err))
			return
		}
		for _, e := range m.Files {
			total += e.Size
		}
		disks = append(disks, disk{mnt: mnt, manifest: m, files: files})
	}
	logf("verify: %d disk(s), %.1f GiB expected, manifests from run %s",
		len(disks), float64(total)/(1<<30), disks[0].manifest.RunID)

	var prog progress
	prog.startJob(total)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); reportLoop(client, &prog, total, stop) }()

	// Rolling throughput for the progress display.
	var mu sync.Mutex
	var done int64
	lastT, lastB := time.Now(), int64(0)
	onBytes := func(n int64) {
		mu.Lock()
		done += n
		if dt := time.Since(lastT); dt >= time.Second {
			prog.update(float64(done)/float64(total), float64(done-lastB)/1e6/dt.Seconds())
			lastT, lastB = time.Now(), done
		}
		mu.Unlock()
	}

	var problems []string
	addProblem := func(f string, args ...any) { problems = append(problems, fmt.Sprintf(f, args...)) }
	verifyErr := func() error {
		for i, d := range disks {
			onDisk := make(map[string]bool, len(d.files))
			for _, rel := range d.files {
				onDisk[rel] = true
				entry, ok := d.manifest.Files[rel]
				if !ok {
					addProblem("disk%d %s: unexpected file (not in manifest)", i, rel)
					continue
				}
				size, sum, err := hashFile(filepath.Join(d.mnt, rel), onBytes)
				if err != nil {
					return fmt.Errorf("disk%d %s: %w", i, rel, err)
				}
				if size != entry.Size {
					addProblem("disk%d %s: size %d, expected %d", i, rel, size, entry.Size)
				} else if sum != entry.SHA256 {
					addProblem("disk%d %s: checksum mismatch", i, rel)
				}
			}
			for rel := range d.manifest.Files {
				if !onDisk[rel] {
					addProblem("disk%d %s: missing", i, rel)
				}
			}
		}
		return nil
	}()

	prog.update(1, 0)
	close(stop)
	wg.Wait()
	if verifyErr != nil {
		logf("verify failed: %v", verifyErr)
		client.reportError(verifyErr.Error())
		return
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		logf("verify FAILED: %d problem(s)", len(problems))
		for _, p := range problems {
			logf("  %s", p)
		}
		shown := problems
		if len(shown) > 8 {
			shown = append(append([]string{}, shown[:8]...),
				fmt.Sprintf("… and %d more (see VM console)", len(problems)-8))
		}
		client.reportError(fmt.Sprintf("verification failed, %d problem(s): %s",
			len(problems), strings.Join(shown, "; ")))
		return
	}
	files := 0
	for _, d := range disks {
		files += len(d.manifest.Files)
	}
	logf("verify OK: %d file(s), %.1f GiB match run %s", files, float64(done)/(1<<30), disks[0].manifest.RunID)
	client.reportDone(done)
}
