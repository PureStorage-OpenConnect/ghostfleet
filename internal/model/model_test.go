package model

import (
	"strings"
	"testing"
	"time"
)

func validSpec() ProfileSpec {
	s := ProfileSpec{
		VMCount:              12,
		NamePrefix:           "bkupsrc",
		DisksPerVM:           2,
		DiskSizeGiB:          100,
		DataPerDiskGiB:       50,
		CompressPercent:      50,
		DedupePercent:        10,
		CrossVMDedupePercent: 20,
		ChangePercent:        5,
		GrowthPercent:        2,
	}
	s.ApplyDefaults()
	return s
}

func TestValidateOK(t *testing.T) {
	s := validSpec()
	if err := s.Validate(); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
}

func TestApplyDefaults(t *testing.T) {
	s := validSpec()
	if s.NamePad != 4 || s.VCPUs != 2 || s.MemoryMiB != 2048 || s.AfterFill != AfterFillShutdown {
		t.Fatalf("defaults not applied: %+v", s)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ProfileSpec)
		want   string
	}{
		{"zero VMs", func(s *ProfileSpec) { s.VMCount = 0 }, "vmCount"},
		{"bad prefix", func(s *ProfileSpec) { s.NamePrefix = "UPPER" }, "namePrefix"},
		{"empty prefix", func(s *ProfileSpec) { s.NamePrefix = "" }, "namePrefix"},
		{"data exceeds disk", func(s *ProfileSpec) { s.DataPerDiskGiB = s.DiskSizeGiB + 1 }, "dataPerDiskGiB"},
		{"compress out of range", func(s *ProfileSpec) { s.CompressPercent = 101 }, "compressPercent"},
		{"dedup sum over 100", func(s *ProfileSpec) { s.DedupePercent, s.CrossVMDedupePercent = 60, 50 }, "must not exceed 100"},
		{"bad afterFill", func(s *ProfileSpec) { s.AfterFill = "reboot" }, "afterFill"},
		{"tag with spaces", func(s *ProfileSpec) { s.Tag = "backup = fleet" }, "tag"},
		{"tag with two equals", func(s *ProfileSpec) { s.Tag = "a=b=c" }, "tag"},
		{"parallel exceeds count", func(s *ProfileSpec) { s.MaxParallelVMs = s.VMCount + 1 }, "maxParallelVMs"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := validSpec()
			tc.mutate(&s)
			err := s.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestFractionalPercentagesAndTags(t *testing.T) {
	s := validSpec()
	// 10 %/year growth simulated as daily incremental runs (DD-5).
	s.GrowthPercent = 0.0261
	s.ChangePercent = 7.5
	s.CompressPercent = 33.3
	s.Tag = "backup=ghostfleet"
	if err := s.Validate(); err != nil {
		t.Fatalf("fractional spec rejected: %v", err)
	}
	s.Tag = "ghostfleet" // plain names stay valid
	if err := s.Validate(); err != nil {
		t.Fatalf("plain tag rejected: %v", err)
	}
}

func TestVMName(t *testing.T) {
	s := validSpec()
	if got := s.VMName(7); got != "bkupsrc-0007" {
		t.Fatalf("VMName(7) = %q, want bkupsrc-0007", got)
	}
	s.NamePad = 2
	if got := s.VMName(123); got != "bkupsrc-123" {
		t.Fatalf("VMName overflows pad: got %q, want bkupsrc-123", got)
	}
}

func TestPlacementDatastores(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"", nil},
		{"LocalDS_0", []string{"LocalDS_0"}},
		{"ds-a\nds-b\nds-c", []string{"ds-a", "ds-b", "ds-c"}},
		{"  ds-a \n\n ds-b ", []string{"ds-a", "ds-b"}}, // trims and drops blanks
	}
	for _, c := range cases {
		p := Placement{"datastore": c.raw}
		got := p.Datastores()
		if strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Errorf("Datastores(%q) = %v, want %v", c.raw, got, c.want)
		}
	}

	// WithDatastore pins one datastore and leaves other keys intact.
	p := Placement{"cluster": "C0", "datastore": "ds-a\nds-b"}
	got := p.WithDatastore("ds-b")
	if got["datastore"] != "ds-b" || got["cluster"] != "C0" {
		t.Errorf("WithDatastore = %v", got)
	}
	if p["datastore"] != "ds-a\nds-b" {
		t.Errorf("WithDatastore mutated original: %v", p)
	}
}

func TestTotalDataGiB(t *testing.T) {
	s := validSpec()
	if got := s.TotalDataGiB(); got != 12*2*50 {
		t.Fatalf("TotalDataGiB = %d, want %d", got, 12*2*50)
	}
}

func validSchedule() Schedule {
	return Schedule{
		DeploymentID: "d1", Action: RunIncremental,
		Kind: ScheduleEvery, Spec: "24h", Enabled: true,
	}
}

func TestScheduleValidate(t *testing.T) {
	sc := validSchedule()
	if err := sc.Validate(); err != nil {
		t.Fatalf("valid schedule rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Schedule)
		want   string
	}{
		{"bad action", func(s *Schedule) { s.Action = RunDeploy }, "action"},
		{"bad kind", func(s *Schedule) { s.Kind = "cron" }, "kind"},
		{"bad duration", func(s *Schedule) { s.Spec = "soon" }, "duration"},
		{"interval too short", func(s *Schedule) { s.Spec = "5s" }, "at least"},
		{"bad daily time", func(s *Schedule) { s.Kind, s.Spec = ScheduleDaily, "25:00" }, "daily"},
		{"bad weekday", func(s *Schedule) { s.Kind, s.Spec = ScheduleDaily, "06:00@Mun" }, "weekday"},
		{"bad timezone", func(s *Schedule) { s.Kind, s.Spec, s.Timezone = ScheduleDaily, "06:00", "Mars/Olympus" }, "timezone"},
		{"bad once time", func(s *Schedule) { s.Kind, s.Spec = ScheduleOnce, "tomorrow" }, "RFC3339"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sc := validSchedule()
			tc.mutate(&sc)
			err := sc.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestScheduleNextAfter(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC) // a Monday

	sc := Schedule{Kind: ScheduleEvery, Spec: "24h"}
	next, ok := sc.NextAfter(now)
	if !ok || !next.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("every 24h: got %v ok=%v", next, ok)
	}

	// Daily at 06:00 UTC: 06:00 already passed today, so tomorrow.
	sc = Schedule{Kind: ScheduleDaily, Spec: "06:00"}
	next, ok = sc.NextAfter(now)
	want := time.Date(2026, 9, 1, 6, 0, 0, 0, time.UTC)
	if !ok || !next.Equal(want) {
		t.Fatalf("daily 06:00: got %v, want %v", next, want)
	}

	// Daily at 14:00 still ahead today.
	sc = Schedule{Kind: ScheduleDaily, Spec: "14:00"}
	next, ok = sc.NextAfter(now)
	want = time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	if !ok || !next.Equal(want) {
		t.Fatalf("daily 14:00: got %v, want %v", next, want)
	}

	// Weekday filter: next Friday 06:00.
	sc = Schedule{Kind: ScheduleDaily, Spec: "06:00@Fri"}
	next, ok = sc.NextAfter(now)
	want = time.Date(2026, 9, 4, 6, 0, 0, 0, time.UTC)
	if !ok || !next.Equal(want) {
		t.Fatalf("daily 06:00@Fri: got %v, want %v", next, want)
	}

	// Timezone: 06:00 Berlin (CEST, UTC+2) tomorrow = 04:00 UTC.
	sc = Schedule{Kind: ScheduleDaily, Spec: "06:00", Timezone: "Europe/Berlin"}
	next, ok = sc.NextAfter(now)
	want = time.Date(2026, 9, 1, 4, 0, 0, 0, time.UTC)
	if !ok || !next.Equal(want) {
		t.Fatalf("daily 06:00 Berlin: got %v, want %v", next, want)
	}

	// Once in the future fires at its time; once in the past never again.
	sc = Schedule{Kind: ScheduleOnce, Spec: "2026-09-30T12:00:00Z"}
	next, ok = sc.NextAfter(now)
	if !ok || !next.Equal(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("once future: got %v ok=%v", next, ok)
	}
	sc = Schedule{Kind: ScheduleOnce, Spec: "2026-08-30T12:00:00Z"}
	if _, ok = sc.NextAfter(now); ok {
		t.Fatal("once past: expected no next occurrence")
	}
}
