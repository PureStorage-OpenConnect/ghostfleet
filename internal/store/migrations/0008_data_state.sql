-- On-disk data state per managed VM, as reported by the agent at boot
-- (register), recorded when a fill/incremental completes, or taken from the
-- discovery report at adoption. Lets "filled" follow the disks, not just this
-- controller's run history.
ALTER TABLE managed_vms ADD COLUMN data_state TEXT NOT NULL DEFAULT '';
ALTER TABLE managed_vms ADD COLUMN data_run_id TEXT NOT NULL DEFAULT '';
ALTER TABLE managed_vms ADD COLUMN data_manifest INTEGER NOT NULL DEFAULT 0;
ALTER TABLE managed_vms ADD COLUMN data_seen_at TEXT;
