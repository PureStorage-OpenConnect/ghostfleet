// Package datagen turns a deployment's effective config into per-VM work
// orders: the recipe the in-VM agent executes with fio. The controller only
// *plans* here (seeds, byte splits, fio knobs) — it never generates payload
// data itself (GEN-7). Seeding is deterministic given the run so runs are
// reproducible from the DB, but scoped per run so every fill generates new,
// unique data; cross-VM dedup within a run stays exact (the fio spike
// confirmed seed→bytes identity).
package datagen

import (
	"hash/fnv"
	"strconv"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

// FileSizeBytes is the size of each generated file. Large files keep fio
// efficient; the organizing layout (global/local/growth dirs) is about
// provenance, not file-count realism (DD-17).
const FileSizeBytes int64 = 1 << 30 // 1 GiB

// BlockSize is fio's write block size. Duplicate 1 MiB buffers contain
// identical sub-blocks, so dedup appliances at any chunk size ≤ 1 MiB still
// collapse them (verified in the calibration spike).
const BlockSize = "1M"

const gib = int64(1) << 30

// DiskWork is the per-disk portion of a work order. The agent partitions the
// disk (GPT), makes an XFS filesystem, and writes Global+Local files.
type DiskWork struct {
	Index       int    `json:"index"`       // 0-based; agent maps to /dev/sdb, /dev/sdc, …
	SizeGiB     int    `json:"sizeGiB"`     // whole-disk size (for the partition)
	GlobalBytes int64  `json:"globalBytes"` // written from the deployment+run seed (cross-VM dedup)
	LocalBytes  int64  `json:"localBytes"`  // written from the per-VM+run seed (unique)
	GlobalSeed  uint64 `json:"globalSeed"`  // identical across VMs for this disk index; new per run
	LocalSeed   uint64 `json:"localSeed"`   // unique per VM+disk; new per run
}

// IdentityFile is written at each data disk's root (next to the run marker):
// a JSON IdentityMarker identifying the disk's VM, deployment and effective
// spec. It survives whatever happens to the VM shell (backup + restore, a
// re-registered VM, a changed MAC), so a disk can always be traced back to —
// or adopted into — a deployment from its content alone.
const IdentityFile = ".ghostfleet-id"

// ManifestFile is written at each data disk's root after every completed
// fill/incremental: a JSON Manifest with per-file SHA-256 checksums of the
// generated data. A verify run re-reads the files and compares against it,
// proving a backup/restore round-tripped the data intact.
const ManifestFile = ".ghostfleet-manifest"

// IdentityMarker identifies a data disk's place in the fleet. DiskIndex is
// filled in per disk by the agent; the rest comes from the work order.
type IdentityMarker struct {
	DeploymentID   string            `json:"deploymentId"`
	DeploymentName string            `json:"deploymentName"`
	VMName         string            `json:"vmName"`
	DiskIndex      int               `json:"diskIndex"`
	Spec           model.ProfileSpec `json:"spec"` // effective config, for adoption
}

// ManifestEntry is one generated file's recorded size and checksum.
type ManifestEntry struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Manifest records the expected content of one data disk after a run: every
// generated file (path relative to the disk root) with size and SHA-256.
type Manifest struct {
	RunID string                   `json:"runId"` // run that last updated the manifest
	Files map[string]ManifestEntry `json:"files"`
}

// DiscoveryDisk is the agent's inspection result for one disk of a
// discovered VM.
type DiscoveryDisk struct {
	Device     string          `json:"device"`  // e.g. sdb
	SizeGiB    int             `json:"sizeGiB"` // whole-disk size
	Filesystem string          `json:"filesystem,omitempty"`
	Identity   *IdentityMarker `json:"identity,omitempty"` // nil: not a GhostFleet data disk
	RunID      string          `json:"runId,omitempty"`    // last completed run per the run marker
	RunBytes   int64           `json:"runBytes,omitempty"` // byte total recorded with it
	Manifest   bool            `json:"manifest"`           // checksum manifest present (verify possible)
	FileCount  int             `json:"fileCount,omitempty"`
}

// DiscoveryReport is what a discovery-booted agent reports after inspecting
// all attached disks (read-only).
type DiscoveryReport struct {
	Disks []DiscoveryDisk `json:"disks"`
}

// DataState summarises a disk inspection into a ManagedVM data state
// (model.Data*): filled when every disk carries GhostFleet data (an identity
// or run marker), empty when none does, partial in between, unknown when
// nothing was inspected. It also returns the run ID recorded on the disks
// (the first marked disk's run marker) and whether every marked disk carries
// a checksum manifest, i.e. whether a verify run is possible.
func DataState(disks []DiscoveryDisk) (state, runID string, manifest bool) {
	if len(disks) == 0 {
		return model.DataUnknown, "", false
	}
	marked := 0
	manifest = true
	for _, d := range disks {
		if d.Identity == nil && d.RunID == "" {
			continue
		}
		marked++
		if runID == "" {
			runID = d.RunID
		}
		manifest = manifest && d.Manifest
	}
	switch marked {
	case 0:
		return model.DataEmpty, "", false
	case len(disks):
		return model.DataFilled, runID, manifest
	default:
		return model.DataPartial, runID, manifest
	}
}

// GhostIdentity returns the identity marker of the first GhostFleet-marked
// disk, or nil — a discovered VM's identity for adoption decisions.
func (r *DiscoveryReport) GhostIdentity() *IdentityMarker {
	for _, d := range r.Disks {
		if d.Identity != nil {
			return d.Identity
		}
	}
	return nil
}

// WorkOrder is the full recipe handed to one VM's agent at registration.
type WorkOrder struct {
	RunID           string     `json:"runId"`
	RunType         string     `json:"runType"` // initial-fill | incremental | verify
	VMName          string     `json:"vmName"`
	Filesystem      string     `json:"filesystem"`      // "xfs"
	CompressPercent int        `json:"compressPercent"` // fio buffer_compress_percentage (0–100)
	DedupePercent   int        `json:"dedupePercent"`   // fio dedupe_percentage (0–100)
	BlockSize       string     `json:"blockSize"`       // fio bs
	FileSizeBytes   int64      `json:"fileSizeBytes"`
	RateLimitKBps   int        `json:"rateLimitKBps,omitempty"` // per-VM share of the deployment cap; 0 = unlimited
	Disks           []DiskWork `json:"disks"`
	// Incremental knobs (RunType == incremental); applied per run (DD-5).
	ChangePercent float64 `json:"changePercent,omitempty"`
	GrowthPercent float64 `json:"growthPercent,omitempty"`
	// RunSeed seeds incremental content; unique per run so each incremental
	// rewrites changed files with genuinely new (CBT-visible) data.
	RunSeed uint64 `json:"runSeed"`
	// Identity is stamped onto each disk (IdentityFile) when a fill or
	// incremental completes, so the disks stay traceable/adoptable even if
	// the VM shell is lost (DiskIndex is set per disk by the agent).
	Identity *IdentityMarker `json:"identity,omitempty"`
}

// seed derives a stable seed from its parts via FNV-1a, masked to 63 bits so
// it fits fio's randseed parser (which rejects values above int64 max).
func seed(parts ...string) uint64 {
	h := fnv.New64a()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0}) // separator so ("a","b") != ("ab")
	}
	return h.Sum64() & 0x7FFFFFFFFFFFFFFF
}

// BuildWorkOrder computes the work order for one VM of a deployment.
//
// Per disk the payload (dataPerDiskGiB) is split: crossVMDedupePercent of it
// is "global" — written from a seed that depends on the deployment, run, and
// disk index, so disk N is byte-identical on every VM of a given run (cross-VM
// dedup) but new on every fill. The rest is "local" — seeded per VM+run+disk,
// hence unique per VM and per run. Within either portion,
// fio's dedupe_percentage creates intra-VM duplicate blocks and
// buffer_compress_percentage sets compressibility.
func BuildWorkOrder(deploymentID, deploymentName, runID, runType, vmName string, spec model.ProfileSpec) WorkOrder {
	wo := WorkOrder{
		RunID:           runID,
		RunType:         runType,
		VMName:          vmName,
		Filesystem:      "xfs",
		CompressPercent: pct(spec.CompressPercent),
		DedupePercent:   pct(spec.DedupePercent),
		BlockSize:       BlockSize,
		FileSizeBytes:   FileSizeBytes,
		ChangePercent:   spec.ChangePercent,
		GrowthPercent:   spec.GrowthPercent,
		RunSeed:         seed(deploymentID, "run", runID),
		Identity: &IdentityMarker{
			DeploymentID:   deploymentID,
			DeploymentName: deploymentName,
			VMName:         vmName,
			Spec:           spec,
		},
	}

	dataBytes := int64(spec.DataPerDiskGiB) * gib
	globalBytes := int64(float64(dataBytes) * spec.CrossVMDedupePercent / 100.0)
	localBytes := dataBytes - globalBytes

	for i := 0; i < spec.DisksPerVM; i++ {
		di := strconv.Itoa(i)
		wo.Disks = append(wo.Disks, DiskWork{
			Index:       i,
			SizeGiB:     spec.DiskSizeGiB,
			GlobalBytes: globalBytes,
			LocalBytes:  localBytes,
			// Global seed excludes vmName → identical across VMs within a
			// run; includes runID → new content on every fill.
			GlobalSeed: seed(deploymentID, "global", runID, di),
			// Local seed includes vmName and runID → unique per VM and per run.
			LocalSeed: seed(deploymentID, vmName, "local", runID, di),
		})
	}

	// RateLimitKBps is filled in by the orchestrator, which knows how many
	// VMs write concurrently and thus each VM's share of the deployment cap.
	return wo
}

// pct clamps and rounds a fractional percent to fio's integer 0–100 knob.
func pct(v float64) int {
	r := int(v + 0.5)
	if r < 0 {
		return 0
	}
	if r > 100 {
		return 100
	}
	return r
}
