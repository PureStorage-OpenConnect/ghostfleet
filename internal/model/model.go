// Package model defines GhostFleet's domain types: source data profiles
// (versioned templates), hypervisor connections, deployments (independent
// instances with an effective configuration) and runs. See
// docs/ARCHITECTURE.md §2 and docs/REQUIREMENTS.md.
package model

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ProfileSpec is the full set of source-data settings. It is stored as an
// immutable snapshot per profile version (PRO-4/PRO-8) and copied — with
// deploy-time overrides applied — into a deployment's effective config.
type ProfileSpec struct {
	// Fleet shape (PRO-1).
	VMCount    int    `json:"vmCount"`
	NamePrefix string `json:"namePrefix"`          // VM names: prefix + zero-padded counter (DD-13)
	NamePad    int    `json:"namePad,omitempty"`   // counter width, default 4
	VCPUs      int    `json:"vcpus,omitempty"`     // default 2 (PRO-11)
	MemoryMiB  int    `json:"memoryMiB,omitempty"` // default 2048 (PRO-11)

	// Disks (PRO-1, PRO-11).
	DisksPerVM     int  `json:"disksPerVM"`
	DiskSizeGiB    int  `json:"diskSizeGiB"`
	DataPerDiskGiB int  `json:"dataPerDiskGiB"`
	Thick          bool `json:"thick,omitempty"` // thin is the default

	// Data characteristics (PRO-2). Percentages are 0–100 and accept
	// fractions (e.g. 0.02 for daily runs simulating 10 %/year growth).
	// Note: compress/dedupe values are rounded to whole percent at
	// execution time when handed to fio (its knobs are integers); change
	// and growth are applied with full precision by the agent.
	CompressPercent      float64 `json:"compressPercent"`      // share of compressible data (maps to fio buffer_compress_percentage)
	DedupePercent        float64 `json:"dedupePercent"`        // duplicate blocks within a VM
	CrossVMDedupePercent float64 `json:"crossVMDedupePercent"` // blocks identical across all VMs of the deployment
	ChangePercent        float64 `json:"changePercent"`        // % of existing data rewritten per incremental run (DD-5)
	GrowthPercent        float64 `json:"growthPercent"`        // % of new data added per incremental run (DD-5)

	// Behavior.
	// Tag is attached to all VMs (PRO-3). Either a plain name
	// ("ghostfleet") or key=value ("backup=ghostfleet"); on vSphere the
	// key becomes the tag category, the value the tag name.
	Tag            string `json:"tag,omitempty"`
	AfterFill      string `json:"afterFill,omitempty"`      // "shutdown" (default) | "keep-running" (PRO-5)
	RateLimitMBps  int    `json:"rateLimitMBps,omitempty"`  // aggregate cap per deployment, 0 = unlimited (PRO-12)
	MaxParallelVMs int    `json:"maxParallelVMs,omitempty"` // staggering, 0 = all at once (PRO-12)
}

const (
	AfterFillShutdown    = "shutdown"
	AfterFillKeepRunning = "keep-running"
)

var namePrefixRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// tagRe allows a plain tag name or one key=value pair.
var tagRe = regexp.MustCompile(`^[\w.-]+(=[\w.-]+)?$`)

// ApplyDefaults fills zero-valued optional fields with their documented defaults.
func (s *ProfileSpec) ApplyDefaults() {
	if s.NamePad == 0 {
		s.NamePad = 4
	}
	if s.VCPUs == 0 {
		s.VCPUs = 2
	}
	if s.MemoryMiB == 0 {
		s.MemoryMiB = 2048
	}
	if s.AfterFill == "" {
		s.AfterFill = AfterFillShutdown
	}
}

// Validate checks the spec against the documented constraints. It does not
// mutate the spec; call ApplyDefaults first.
func (s *ProfileSpec) Validate() error {
	switch {
	case s.VMCount < 1 || s.VMCount > 1000:
		return fmt.Errorf("vmCount must be 1–1000, got %d", s.VMCount)
	case !namePrefixRe.MatchString(s.NamePrefix):
		return fmt.Errorf("namePrefix %q must match %s", s.NamePrefix, namePrefixRe)
	case s.NamePad < 1 || s.NamePad > 8:
		return fmt.Errorf("namePad must be 1–8, got %d", s.NamePad)
	case s.VCPUs < 1 || s.VCPUs > 64:
		return fmt.Errorf("vcpus must be 1–64, got %d", s.VCPUs)
	case s.MemoryMiB < 512 || s.MemoryMiB > 262144:
		return fmt.Errorf("memoryMiB must be 512–262144, got %d", s.MemoryMiB)
	case s.DisksPerVM < 1 || s.DisksPerVM > 60:
		return fmt.Errorf("disksPerVM must be 1–60, got %d", s.DisksPerVM)
	case s.DiskSizeGiB < 1 || s.DiskSizeGiB > 62*1024:
		return fmt.Errorf("diskSizeGiB must be 1–63488, got %d", s.DiskSizeGiB)
	case s.DataPerDiskGiB < 0 || s.DataPerDiskGiB > s.DiskSizeGiB:
		return fmt.Errorf("dataPerDiskGiB must be 0–diskSizeGiB (%d), got %d", s.DiskSizeGiB, s.DataPerDiskGiB)
	case s.CompressPercent < 0 || s.CompressPercent > 100:
		return fmt.Errorf("compressPercent must be 0–100, got %g", s.CompressPercent)
	case s.DedupePercent < 0 || s.DedupePercent > 100:
		return fmt.Errorf("dedupePercent must be 0–100, got %g", s.DedupePercent)
	case s.CrossVMDedupePercent < 0 || s.CrossVMDedupePercent > 100:
		return fmt.Errorf("crossVMDedupePercent must be 0–100, got %g", s.CrossVMDedupePercent)
	case s.DedupePercent+s.CrossVMDedupePercent > 100:
		return fmt.Errorf("dedupePercent + crossVMDedupePercent must not exceed 100, got %g",
			s.DedupePercent+s.CrossVMDedupePercent)
	case s.ChangePercent < 0 || s.ChangePercent > 100:
		return fmt.Errorf("changePercent must be 0–100, got %g", s.ChangePercent)
	case s.GrowthPercent < 0 || s.GrowthPercent > 1000:
		return fmt.Errorf("growthPercent must be 0–1000, got %g", s.GrowthPercent)
	case s.Tag != "" && !tagRe.MatchString(s.Tag):
		return fmt.Errorf("tag %q must be a plain name or key=value (letters, digits, . _ -)", s.Tag)
	case s.AfterFill != AfterFillShutdown && s.AfterFill != AfterFillKeepRunning:
		return fmt.Errorf("afterFill must be %q or %q, got %q", AfterFillShutdown, AfterFillKeepRunning, s.AfterFill)
	case s.RateLimitMBps < 0:
		return fmt.Errorf("rateLimitMBps must be >= 0, got %d", s.RateLimitMBps)
	case s.MaxParallelVMs < 0 || s.MaxParallelVMs > s.VMCount:
		return fmt.Errorf("maxParallelVMs must be 0–vmCount (%d), got %d", s.VMCount, s.MaxParallelVMs)
	}
	return nil
}

// VMName returns the name of the n-th VM (1-based) per the naming scheme.
func (s *ProfileSpec) VMName(n int) string {
	return fmt.Sprintf("%s-%0*d", s.NamePrefix, s.NamePad, n)
}

// TotalDataGiB is the amount of payload data across the whole fleet.
func (s *ProfileSpec) TotalDataGiB() int {
	return s.VMCount * s.DisksPerVM * s.DataPerDiskGiB
}

// Profile is a named, versioned template (PRO-4, PRO-8).
type Profile struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	CurrentVersion int         `json:"currentVersion"`
	Spec           ProfileSpec `json:"spec"` // spec of CurrentVersion
	CreatedAt      time.Time   `json:"createdAt"`
	UpdatedAt      time.Time   `json:"updatedAt"`
}

// ProfileVersion is one immutable snapshot of a profile's spec.
type ProfileVersion struct {
	ProfileID string      `json:"profileId"`
	Version   int         `json:"version"`
	Spec      ProfileSpec `json:"spec"`
	CreatedAt time.Time   `json:"createdAt"`
}

// Connection is a hypervisor management connection (HYP-4). The secret is
// write-only: accepted on create/update, stored encrypted, never returned.
type Connection struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Plugin      string    `json:"plugin"` // e.g. "vsphere"
	Endpoint    string    `json:"endpoint"`
	Username    string    `json:"username"`
	InsecureTLS bool      `json:"insecureTLS"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// KnownPlugins lists implemented hypervisor plugins. Only vSphere for now
// (DD-8); the list grows as plugins land.
var KnownPlugins = []string{"vsphere"}

// Placement is the deploy-time compute/storage selection (PRO-9). The keys
// are plugin-specific (for vSphere: cluster, resourcePool, datastore, network).
type Placement map[string]string

// Datastores returns the datastore name(s) selected for VM placement. Multiple
// datastores (over which the orchestrator round-robins the VMs) are stored
// newline-separated in the "datastore" key; the common single-datastore case
// has no separator and so is byte-identical to older deployments. Empty if no
// datastore was selected.
func (p Placement) Datastores() []string {
	var out []string
	for _, s := range strings.Split(p["datastore"], "\n") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// WithDatastore returns a copy of the placement pinned to a single datastore.
// The orchestrator uses it to place one VM on one of several round-robin
// datastores while leaving every other placement key intact; the driver then
// resolves the usual single "datastore" key.
func (p Placement) WithDatastore(name string) Placement {
	cp := make(Placement, len(p))
	for k, v := range p {
		cp[k] = v
	}
	cp["datastore"] = name
	return cp
}

// Deployment is an independent instance created from a profile version plus
// deploy-time overrides (PRO-8/9/10). Spec is its own effective config.
type Deployment struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	ProfileID      string      `json:"profileId,omitempty"` // provenance only
	ProfileVersion int         `json:"profileVersion,omitempty"`
	ConnectionID   string      `json:"connectionId"`
	Spec           ProfileSpec `json:"spec"`
	Placement      Placement   `json:"placement"`
	Status         string      `json:"status"`
	CreatedAt      time.Time   `json:"createdAt"`
	UpdatedAt      time.Time   `json:"updatedAt"`

	// Origin marks how the deployment came to be: created ("" — the normal
	// deploy path) or adopted (built around discovered VMs, e.g. a backup
	// restore; see DiscoveredVM). Adopted deployments own VMs the controller
	// never created, so reconcile (deploy/scale-up) is not applicable to them —
	// fills, incrementals, verify, power and teardown are.
	Origin string `json:"origin,omitempty"`

	// Read-time flags, not persisted: set by the API from run history and the
	// orchestrator's live job state. Filled gates the incremental action;
	// Running drives the cancel affordance.
	Filled  bool `json:"filled"`
	Running bool `json:"running"`
}

// Deployment origins.
const (
	OriginCreated = ""        // normal deploy path
	OriginAdopted = "adopted" // built from discovered VMs (restore adoption)
)

// Conflict-resolution modes for deploy, used when a target VM name already
// exists on the hypervisor owned by another (or an unknown) deployment.
const (
	OnConflictAbort = "abort" // default: fail and report the conflicts
	OnConflictAdopt = "adopt" // take the existing VMs into this deployment
	OnConflictClean = "clean" // delete the conflicting VMs, then create fresh
)

// VMConflict is a target VM name already present on the hypervisor under a
// different (or unknown) deployment — surfaced before deploy so the operator
// chooses adopt/clean/abort instead of a silent takeover.
type VMConflict struct {
	Name       string     `json:"name"`
	Ref        string     `json:"ref"`
	OwnerID    string     `json:"ownerId,omitempty"`   // from the VM annotation
	OwnerName  string     `json:"ownerName,omitempty"` // owner deployment name
	OwnerKnown bool       `json:"ownerKnown"`          // owner still exists on this controller
	OwnerSince *time.Time `json:"ownerSince,omitempty"`
}

// Deployment statuses.
const (
	DeploymentNew       = "new"       // record exists, nothing on the hypervisor yet
	DeploymentDeploying = "deploying" // reconcile job in progress
	DeploymentReady     = "ready"     // VMs exist
	DeploymentError     = "error"     // last job failed; deploy again to retry
	DeploymentDeleting  = "deleting"  // teardown job in progress
	DeploymentDeleted   = "deleted"   // torn down, kept for run history (PRO-7)
)

// ManagedVM is one VM the orchestrator created on the hypervisor. The disk
// shape mirrors what actually exists, used for reconciliation diffs.
type ManagedVM struct {
	ID           string `json:"id"`
	DeploymentID string `json:"deploymentId"`
	Name         string `json:"name"`
	Ref          string `json:"ref"` // driver-specific reference
	DiskCount    int    `json:"diskCount"`
	DiskSizeGiB  int    `json:"diskSizeGiB"`
	MAC          string `json:"mac,omitempty"` // primary NIC, lower-case
	// BootToken authenticates the PXE-booted agent (one per VM, in the
	// kernel cmdline). Never exposed through the management API.
	BootToken   string     `json:"-"`
	AgentStatus string     `json:"agentStatus"` // none | online
	AgentSeenAt *time.Time `json:"agentSeenAt,omitempty"`

	// Fill/incremental progress for the active run (FillRunID).
	FillRunID    string  `json:"fillRunId,omitempty"`
	FillStatus   string  `json:"fillStatus,omitempty"` // pending | working | done | failed
	BytesWritten int64   `json:"bytesWritten"`
	BytesTotal   int64   `json:"bytesTotal"`
	MBps         float64 `json:"mbps"`
	FillError    string  `json:"fillError,omitempty"`

	CreatedAt time.Time `json:"createdAt"`
}

// Agent statuses for ManagedVM.AgentStatus.
const (
	AgentNone   = "none"   // never registered
	AgentOnline = "online" // registered; staleness derived from AgentSeenAt
)

// Per-VM fill statuses for ManagedVM.FillStatus.
const (
	FillPending = "pending" // assigned to a run, agent hasn't started
	FillWorking = "working" // agent is generating data
	FillDone    = "done"    // agent finished this run
	FillFailed  = "failed"  // agent reported an error
)

// Run is one execution against a deployment: initial fill, incremental,
// scale-up or teardown.
type Run struct {
	ID           string     `json:"id"`
	DeploymentID string     `json:"deploymentId"`
	Type         string     `json:"type"`
	Status       string     `json:"status"`
	StartedAt    *time.Time `json:"startedAt,omitempty"`
	FinishedAt   *time.Time `json:"finishedAt,omitempty"`
	Stats        string     `json:"stats,omitempty"` // JSON summary (bytes written, MB/s, ...)
	// TriggeredBy is the ID of the schedule that fired this run; empty for
	// manually started runs.
	TriggeredBy string    `json:"triggeredBy,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
}

const (
	RunDeploy      = "deploy" // create VMs/disks per effective config
	RunInitialFill = "initial-fill"
	RunIncremental = "incremental"
	RunVerify      = "verify" // read back and check data against the on-disk manifest
	RunScaleUp     = "scale-up"
	RunTeardown    = "teardown"
	RunPowerOn     = "power-on"
	RunPowerOff    = "power-off"

	RunPending   = "pending"
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
)

// Schedule kicks off one run type against a deployment on a timer: recurring
// by interval ("every"), recurring at a wall-clock time ("daily") or one-shot
// at an absolute time ("once"). The scheduler fires it through the same
// orchestrator entry points the API uses.
type Schedule struct {
	ID           string `json:"id"`
	DeploymentID string `json:"deploymentId"`
	Action       string `json:"action"` // incremental | verify | power-on | power-off | teardown
	Kind         string `json:"kind"`   // every | daily | once
	// Spec depends on Kind: every = Go duration ("24h"); daily = "HH:MM" with
	// an optional weekday filter ("06:00" or "06:00@Mon,Fri"); once = RFC3339.
	Spec string `json:"spec"`
	// Timezone is the IANA zone the daily wall-clock time is evaluated in
	// (empty = UTC). Ignored for every/once.
	Timezone string `json:"timezone,omitempty"`
	Enabled  bool   `json:"enabled"`
	// NextRunAt is the persisted next fire time — the scheduler's single
	// source of truth, so schedules survive controller restarts. Nil when the
	// schedule will not fire again (a spent one-shot).
	NextRunAt   *time.Time `json:"nextRunAt,omitempty"`
	LastFiredAt *time.Time `json:"lastFiredAt,omitempty"`
	LastRunID   string     `json:"lastRunId,omitempty"`
	LastResult  string     `json:"lastResult,omitempty"` // fired | skipped: ... | error: ...
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// Schedule kinds.
const (
	ScheduleEvery = "every"
	ScheduleDaily = "daily"
	ScheduleOnce  = "once"
)

// ScheduleActions are the run types a schedule may kick off. Deploy and
// initial-fill stay manual: they are one-time setup steps, and adopted
// deployments cannot deploy at all.
var ScheduleActions = []string{RunIncremental, RunVerify, RunPowerOn, RunPowerOff, RunTeardown}

// minEvery keeps interval schedules from degenerating into a busy loop.
const minEvery = time.Minute

// dailyRe matches the daily spec: HH:MM plus an optional weekday filter.
var dailyRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])(@[A-Za-z]{3}(,[A-Za-z]{3})*)?$`)

var weekdayNames = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
	"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
}

// Validate checks action, kind, spec and timezone for consistency.
func (sc *Schedule) Validate() error {
	valid := false
	for _, a := range ScheduleActions {
		if sc.Action == a {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("action must be one of %s, got %q", strings.Join(ScheduleActions, ", "), sc.Action)
	}
	switch sc.Kind {
	case ScheduleEvery:
		d, err := time.ParseDuration(sc.Spec)
		if err != nil {
			return fmt.Errorf("every: spec must be a duration like \"24h\": %v", err)
		}
		if d < minEvery {
			return fmt.Errorf("every: interval must be at least %s, got %s", minEvery, d)
		}
	case ScheduleDaily:
		if _, _, _, err := parseDailySpec(sc.Spec); err != nil {
			return err
		}
		if sc.Timezone != "" {
			if _, err := time.LoadLocation(sc.Timezone); err != nil {
				return fmt.Errorf("timezone: unknown IANA zone %q", sc.Timezone)
			}
		}
	case ScheduleOnce:
		if _, err := time.Parse(time.RFC3339, sc.Spec); err != nil {
			return fmt.Errorf("once: spec must be an RFC3339 timestamp: %v", err)
		}
	default:
		return fmt.Errorf("kind must be %s, %s or %s, got %q", ScheduleEvery, ScheduleDaily, ScheduleOnce, sc.Kind)
	}
	return nil
}

// NextAfter computes the next fire time strictly after now, or ok=false when
// the schedule has no further occurrence (a one-shot whose time has passed).
// Call Validate first; an invalid spec returns ok=false.
func (sc *Schedule) NextAfter(now time.Time) (next time.Time, ok bool) {
	switch sc.Kind {
	case ScheduleEvery:
		d, err := time.ParseDuration(sc.Spec)
		if err != nil || d < minEvery {
			return time.Time{}, false
		}
		return now.Add(d), true
	case ScheduleDaily:
		hour, min, days, err := parseDailySpec(sc.Spec)
		if err != nil {
			return time.Time{}, false
		}
		loc := time.UTC
		if sc.Timezone != "" {
			if l, err := time.LoadLocation(sc.Timezone); err == nil {
				loc = l
			}
		}
		local := now.In(loc)
		// Walk forward day by day (8 covers a single-weekday filter plus DST
		// oddities) until the wall-clock time is in the future and allowed.
		for i := 0; i < 8; i++ {
			day := local.AddDate(0, 0, i)
			cand := time.Date(day.Year(), day.Month(), day.Day(), hour, min, 0, 0, loc)
			if cand.After(now) && (days == nil || days[cand.Weekday()]) {
				return cand.UTC(), true
			}
		}
		return time.Time{}, false
	case ScheduleOnce:
		t, err := time.Parse(time.RFC3339, sc.Spec)
		if err != nil || !t.After(now) {
			return time.Time{}, false
		}
		return t.UTC(), true
	}
	return time.Time{}, false
}

// parseDailySpec splits "HH:MM" or "HH:MM@Mon,Fri" into its parts. A nil days
// map means every weekday is allowed.
func parseDailySpec(spec string) (hour, min int, days map[time.Weekday]bool, err error) {
	m := dailyRe.FindStringSubmatch(spec)
	if m == nil {
		return 0, 0, nil, fmt.Errorf(`daily: spec must be "HH:MM" or "HH:MM@Mon,Fri", got %q`, spec)
	}
	hour, _ = strconv.Atoi(m[1])
	min, _ = strconv.Atoi(m[2])
	if m[3] != "" {
		days = make(map[time.Weekday]bool)
		for _, name := range strings.Split(m[3][1:], ",") {
			wd, ok := weekdayNames[strings.ToLower(name)]
			if !ok {
				return 0, 0, nil, fmt.Errorf("daily: unknown weekday %q (use Mon..Sun)", name)
			}
			days[wd] = true
		}
	}
	return hour, min, days, nil
}

// DiscoveredVM is an unknown machine that PXE-booted on the isolated network
// (typically a backup restore of a GhostFleet VM, which comes up with a fresh
// MAC). With discovery enabled it is booted into the temp OS with a
// report-only discovery token; the agent inspects its disks for GhostFleet
// identity markers and the operator can then adopt it (DiscoveredVM.Status).
type DiscoveredVM struct {
	ID  string `json:"id"`
	MAC string `json:"mac"` // lower-case, unique
	// Token authenticates the discovery agent. It only authorizes inspection
	// reports and heartbeats — never work orders (ARCHITECTURE.md §7). Not
	// exposed through the management API.
	Token       string     `json:"-"`
	Status      string     `json:"status"`               // new | adopted
	Inspection  string     `json:"inspection,omitempty"` // JSON datagen.DiscoveryReport
	FirstSeenAt time.Time  `json:"firstSeenAt"`
	LastSeenAt  time.Time  `json:"lastSeenAt"`
	AgentSeenAt *time.Time `json:"agentSeenAt,omitempty"` // last agent report/heartbeat
}

// DiscoveredVM statuses.
const (
	DiscoveredNew     = "new"     // seen and (maybe) inspected, awaiting operator action
	DiscoveredAdopted = "adopted" // adopted; its agent is told to reboot into managed life
)
