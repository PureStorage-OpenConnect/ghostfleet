package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/datagen"
)

// The manifest records every generated file's size and SHA-256 at each
// disk's root (datagen.ManifestFile). It is written after every completed
// fill/incremental and travels with the disk through backup and restore, so
// a verify run can prove the data round-tripped intact — without the
// controller having to remember anything about the disk.

// dataDirs are the disk-root directories holding generated files.
var dataDirs = []string{"global", "local"}

// Durability: the data files are written by fio with O_DIRECT and end_fsync,
// so they are on the disk the moment fio exits — but a run ends with a hard
// power-off from the controller (afterFill defaults to "shutdown"), which
// gives the guest no chance to flush anything else. A plain buffered write of
// the manifest can therefore still be in the page cache when the power goes,
// leaving the disk with this run's data and the *previous* run's manifest. A
// backup taken from there restores fine and then fails verify with checksum
// mismatches on exactly the files the run rewrote, plus "unexpected file" for
// its fresh growth files — the data is intact, only the record of it is stale.
// So every marker file is written through writeFileSync, and every run ends
// with flushDisks (see gen.go).

// syncDir flushes a directory, making an entry created or renamed inside it
// durable. Fsyncing a file does not make its *name* durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// writeFileSync writes a file and flushes both the content and the directory
// entry, so neither can be lost to a power cut.
func writeFileSync(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// loadManifest reads a disk's manifest, or errors if absent/corrupt.
func loadManifest(mnt string) (*datagen.Manifest, error) {
	data, err := os.ReadFile(filepath.Join(mnt, datagen.ManifestFile))
	if err != nil {
		return nil, err
	}
	var m datagen.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("corrupt manifest: %w", err)
	}
	if m.Files == nil {
		m.Files = map[string]datagen.ManifestEntry{}
	}
	return &m, nil
}

// writeManifest stores a disk's manifest (write + fsync + rename + fsync of
// the disk root, so a crash never leaves a half-written manifest behind and a
// power-off never loses a complete one).
func writeManifest(mnt string, m *datagen.Manifest) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := filepath.Join(mnt, datagen.ManifestFile+".tmp")
	if err := writeFileSync(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(mnt, datagen.ManifestFile)); err != nil {
		return err
	}
	return syncDir(mnt)
}

// writeIdentity stamps the disk with its place in the fleet (datagen
// .IdentityFile) — the piece that makes a restored disk adoptable.
func writeIdentity(mnt string, wo *datagen.WorkOrder, diskIndex int) {
	if wo.Identity == nil {
		return // older controller: no identity in the work order
	}
	id := *wo.Identity
	id.DiskIndex = diskIndex
	data, _ := json.Marshal(id)
	if err := writeFileSync(filepath.Join(mnt, datagen.IdentityFile), data, 0o644); err != nil {
		logf("writing identity marker on %s: %v", mnt, err)
	}
}

// listDataFiles returns the relative paths (e.g. "local/gf_local.0.3") of all
// generated files on a disk, sorted.
func listDataFiles(mnt string) ([]string, error) {
	var out []string
	for _, dir := range dataDirs {
		entries, err := os.ReadDir(filepath.Join(mnt, dir))
		if err != nil {
			if os.IsNotExist(err) {
				continue // e.g. no global region (crossVMDedupePercent = 0)
			}
			return nil, err
		}
		for _, e := range entries {
			if e.Type().IsRegular() {
				out = append(out, dir+"/"+e.Name())
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// hashFile computes a file's size and SHA-256, reporting progress through
// onBytes (may be nil).
func hashFile(path string, onBytes func(int64)) (int64, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 4<<20)
	var total int64
	for {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			total += int64(n)
			if onBytes != nil {
				onBytes(int64(n))
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, "", err
		}
	}
	return total, hex.EncodeToString(h.Sum(nil)), nil
}

// updateDiskManifest brings a disk's manifest up to date after a run: files
// in `changed` (or absent from the old manifest, e.g. fresh growth files) are
// re-hashed, everything else keeps its recorded checksum; entries whose file
// vanished are dropped. With no prior manifest everything is hashed —
// initial fills, and the upgrade path for disks filled before manifests
// existed.
func updateDiskManifest(mnt, runID string, changed map[string]bool) error {
	old, err := loadManifest(mnt)
	if err != nil {
		old = &datagen.Manifest{Files: map[string]datagen.ManifestEntry{}}
	}
	files, err := listDataFiles(mnt)
	if err != nil {
		return err
	}
	m := &datagen.Manifest{RunID: runID, Files: make(map[string]datagen.ManifestEntry, len(files))}
	var hashed int64
	start := time.Now()
	for _, rel := range files {
		if entry, ok := old.Files[rel]; ok && !changed[rel] {
			m.Files[rel] = entry
			continue
		}
		size, sum, err := hashFile(filepath.Join(mnt, rel), nil)
		if err != nil {
			return fmt.Errorf("hashing %s: %w", rel, err)
		}
		m.Files[rel] = datagen.ManifestEntry{Size: size, SHA256: sum}
		hashed += size
	}
	if hashed > 0 {
		logf("manifest %s: hashed %.1f GiB in %.0fs (%d files total)",
			mnt, float64(hashed)/(1<<30), time.Since(start).Seconds(), len(m.Files))
	}
	return writeManifest(mnt, m)
}
