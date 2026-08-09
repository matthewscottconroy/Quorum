-- Persisted last-success marker for background jobs, so the in-process
-- scheduler can detect a run missed while the process was down (an upgrade or
-- outage across the scheduled time) and catch up on startup.
CREATE TABLE job_runs (
    job             TEXT PRIMARY KEY,
    last_success_at TIMESTAMPTZ NOT NULL
);
