CREATE TABLE schedules (
    id            TEXT PRIMARY KEY,
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    action        TEXT NOT NULL,
    kind          TEXT NOT NULL,
    spec          TEXT NOT NULL,
    timezone      TEXT NOT NULL DEFAULT '',
    enabled       INTEGER NOT NULL DEFAULT 1,
    next_run_at   TEXT,
    last_fired_at TEXT,
    last_run_id   TEXT,
    last_result   TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE INDEX idx_schedules_due ON schedules(enabled, next_run_at);
CREATE INDEX idx_schedules_deployment ON schedules(deployment_id);

-- Provenance: the schedule that fired a run; NULL for manual runs.
ALTER TABLE runs ADD COLUMN triggered_by TEXT;
