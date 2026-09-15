CREATE TABLE connections (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    plugin      TEXT NOT NULL,
    endpoint    TEXT NOT NULL,
    username    TEXT NOT NULL,
    secret_enc  BLOB NOT NULL,
    insecure_tls INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE profiles (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    current_version INTEGER NOT NULL,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE TABLE profile_versions (
    profile_id TEXT NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    version    INTEGER NOT NULL,
    spec       TEXT NOT NULL, -- JSON model.ProfileSpec
    created_at TEXT NOT NULL,
    PRIMARY KEY (profile_id, version)
);

CREATE TABLE deployments (
    id              TEXT PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,
    profile_id      TEXT REFERENCES profiles(id) ON DELETE SET NULL,
    profile_version INTEGER,
    connection_id   TEXT NOT NULL REFERENCES connections(id),
    spec            TEXT NOT NULL, -- JSON model.ProfileSpec (effective config)
    placement       TEXT NOT NULL, -- JSON model.Placement
    status          TEXT NOT NULL,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);

CREATE TABLE runs (
    id            TEXT PRIMARY KEY,
    deployment_id TEXT NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    type          TEXT NOT NULL,
    status        TEXT NOT NULL,
    started_at    TEXT,
    finished_at   TEXT,
    stats         TEXT,
    created_at    TEXT NOT NULL
);

CREATE INDEX idx_runs_deployment ON runs(deployment_id, created_at);
CREATE INDEX idx_deployments_connection ON deployments(connection_id);
