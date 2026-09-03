-- Coordination metadata is separate from public Run snapshots and revisions.
CREATE TABLE perfeng_control.reconciliation_leases (
    run_id text PRIMARY KEY REFERENCES perfeng_control.runs (run_id),
    worker_id text NOT NULL CHECK (worker_id ~ '^[a-zA-Z0-9][a-zA-Z0-9._:-]{0,127}$'),
    token text NOT NULL CHECK (token ~ '^[a-f0-9]{32}$'),
    expires_at timestamptz NOT NULL,
    available_at timestamptz NOT NULL
);
CREATE INDEX reconciliation_leases_due
    ON perfeng_control.reconciliation_leases (available_at, expires_at, run_id);
