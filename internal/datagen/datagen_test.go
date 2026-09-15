package datagen

import (
	"testing"

	"github.com/PureStorage-OpenConnect/ghostfleet/internal/model"
)

func spec() model.ProfileSpec {
	s := model.ProfileSpec{
		VMCount: 3, NamePrefix: "ghost", DisksPerVM: 2,
		DiskSizeGiB: 100, DataPerDiskGiB: 40,
		CompressPercent: 50, DedupePercent: 10, CrossVMDedupePercent: 25,
		ChangePercent: 7.5, GrowthPercent: 0.5,
	}
	s.ApplyDefaults()
	return s
}

func TestByteSplitMatchesPercent(t *testing.T) {
	wo := BuildWorkOrder("dep1", "dep one", "run1", model.RunInitialFill, "ghost-0001", spec())
	if len(wo.Disks) != 2 {
		t.Fatalf("disks = %d, want 2", len(wo.Disks))
	}
	d := wo.Disks[0]
	total := d.GlobalBytes + d.LocalBytes
	wantTotal := int64(40) << 30
	if total != wantTotal {
		t.Fatalf("total bytes = %d, want %d", total, wantTotal)
	}
	// 25% cross-VM dedup → a quarter global.
	if got := float64(d.GlobalBytes) / float64(total) * 100; got < 24.9 || got > 25.1 {
		t.Fatalf("global share = %.2f%%, want ~25%%", got)
	}
}

func TestCrossVMSeedsIdenticalLocalSeedsUnique(t *testing.T) {
	a := BuildWorkOrder("dep1", "dep one", "run1", model.RunInitialFill, "ghost-0001", spec())
	b := BuildWorkOrder("dep1", "dep one", "run1", model.RunInitialFill, "ghost-0002", spec())

	for i := range a.Disks {
		// Global seed per disk index must match across VMs (cross-VM dedup).
		if a.Disks[i].GlobalSeed != b.Disks[i].GlobalSeed {
			t.Errorf("disk %d global seed differs across VMs: %d vs %d",
				i, a.Disks[i].GlobalSeed, b.Disks[i].GlobalSeed)
		}
		// Local seed must differ across VMs (unique data).
		if a.Disks[i].LocalSeed == b.Disks[i].LocalSeed {
			t.Errorf("disk %d local seed identical across VMs (should be unique)", i)
		}
	}
	// Different disks of the same VM must not collide either.
	if a.Disks[0].GlobalSeed == a.Disks[1].GlobalSeed {
		t.Error("global seeds collide across disk indexes")
	}
}

func TestDeterministic(t *testing.T) {
	a := BuildWorkOrder("dep1", "dep one", "run1", model.RunInitialFill, "ghost-0001", spec())
	b := BuildWorkOrder("dep1", "dep one", "run1", model.RunInitialFill, "ghost-0001", spec())
	if a.Disks[0].GlobalSeed != b.Disks[0].GlobalSeed || a.Disks[0].LocalSeed != b.Disks[0].LocalSeed {
		t.Fatal("seed derivation is not deterministic")
	}
	// A different deployment yields different seeds (no cross-deployment dedup).
	c := BuildWorkOrder("dep2", "dep two", "run1", model.RunInitialFill, "ghost-0001", spec())
	if a.Disks[0].GlobalSeed == c.Disks[0].GlobalSeed {
		t.Fatal("global seed should depend on deployment ID")
	}
}

func TestRunSeedsDiffer(t *testing.T) {
	// Same deployment and VM, different runs → both regions get new data so a
	// re-fill is not byte-identical to the previous one.
	a := BuildWorkOrder("dep1", "dep one", "run1", model.RunInitialFill, "ghost-0001", spec())
	b := BuildWorkOrder("dep1", "dep one", "run2", model.RunInitialFill, "ghost-0001", spec())
	if a.Disks[0].GlobalSeed == b.Disks[0].GlobalSeed {
		t.Error("global seed identical across runs (should be new per run)")
	}
	if a.Disks[0].LocalSeed == b.Disks[0].LocalSeed {
		t.Error("local seed identical across runs (should be new per run)")
	}
}

func TestFractionalCompressRounds(t *testing.T) {
	s := spec()
	s.CompressPercent = 33.3
	s.DedupePercent = 0.4
	wo := BuildWorkOrder("dep1", "dep one", "run1", model.RunInitialFill, "ghost-0001", s)
	if wo.CompressPercent != 33 {
		t.Errorf("compress rounded to %d, want 33", wo.CompressPercent)
	}
	if wo.DedupePercent != 0 {
		t.Errorf("dedupe rounded to %d, want 0", wo.DedupePercent)
	}
	// Fractional change/growth pass through with full precision.
	if wo.ChangePercent != 7.5 {
		t.Errorf("changePercent = %v, want 7.5", wo.ChangePercent)
	}
}
