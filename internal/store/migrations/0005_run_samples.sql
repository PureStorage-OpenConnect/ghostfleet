-- Coarse throughput time series per run, for live and historical charts.
CREATE TABLE run_samples (
    run_id        TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    ts            TEXT NOT NULL,  -- RFC3339
    mbps          REAL NOT NULL,
    bytes_written INTEGER NOT NULL
);

CREATE INDEX idx_run_samples_run ON run_samples(run_id, ts);
