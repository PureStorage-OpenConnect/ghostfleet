CREATE TABLE managed_vms (
    id            TEXT PRIMARY KEY,
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    ref           TEXT NOT NULL, -- driver-specific reference (vSphere moref)
    disk_count    INTEGER NOT NULL,
    disk_size_gib INTEGER NOT NULL,
    created_at    TEXT NOT NULL,
    UNIQUE (deployment_id, name)
);

CREATE INDEX idx_managed_vms_deployment ON managed_vms(deployment_id);
