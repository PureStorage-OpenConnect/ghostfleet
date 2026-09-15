-- Per-VM progress for the active fill/incremental run. Lets the work-order
-- endpoint know what to hand each agent and drives live throughput stats.
ALTER TABLE managed_vms ADD COLUMN fill_run_id   TEXT NOT NULL DEFAULT '';
ALTER TABLE managed_vms ADD COLUMN fill_status   TEXT NOT NULL DEFAULT '';  -- '' | pending | working | done | failed
ALTER TABLE managed_vms ADD COLUMN bytes_written INTEGER NOT NULL DEFAULT 0;
ALTER TABLE managed_vms ADD COLUMN bytes_total   INTEGER NOT NULL DEFAULT 0;
ALTER TABLE managed_vms ADD COLUMN mbps          REAL NOT NULL DEFAULT 0;
ALTER TABLE managed_vms ADD COLUMN fill_error    TEXT NOT NULL DEFAULT '';
ALTER TABLE managed_vms ADD COLUMN fill_updated_at TEXT;
