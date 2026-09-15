-- Discovery/adoption of unknown VMs (e.g. backup restores of GhostFleet VMs,
-- which come up with a fresh MAC and so don't match any managed VM).
CREATE TABLE discovered_vms (
    id            TEXT PRIMARY KEY,
    mac           TEXT NOT NULL UNIQUE,   -- lower-case
    token         TEXT NOT NULL,          -- report-only discovery token
    status        TEXT NOT NULL DEFAULT 'new',  -- new | adopted
    inspection    TEXT NOT NULL DEFAULT '',     -- JSON datagen.DiscoveryReport
    first_seen_at TEXT NOT NULL,
    last_seen_at  TEXT NOT NULL,
    agent_seen_at TEXT
);

CREATE INDEX idx_discovered_vms_token ON discovered_vms(token);

-- Deployments gain an origin: '' = created by deploy, 'adopted' = built from
-- discovered VMs (restore adoption); adopted deployments are never reconciled.
ALTER TABLE deployments ADD COLUMN origin TEXT NOT NULL DEFAULT '';
