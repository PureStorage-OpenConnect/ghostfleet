ALTER TABLE managed_vms ADD COLUMN mac TEXT NOT NULL DEFAULT '';
ALTER TABLE managed_vms ADD COLUMN boot_token TEXT NOT NULL DEFAULT '';
ALTER TABLE managed_vms ADD COLUMN agent_status TEXT NOT NULL DEFAULT 'none';
ALTER TABLE managed_vms ADD COLUMN agent_seen_at TEXT;

CREATE INDEX idx_managed_vms_token ON managed_vms(boot_token);
CREATE INDEX idx_managed_vms_mac ON managed_vms(mac);
