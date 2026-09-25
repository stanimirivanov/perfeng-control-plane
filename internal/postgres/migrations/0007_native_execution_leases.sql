-- Current native execution ownership. Expired rows are replaced on recovery;
-- plaintext lease capabilities are never persisted.
CREATE TABLE perfeng_control.native_execution_leases (
    run_id text PRIMARY KEY REFERENCES perfeng_control.runs (run_id),
    lease_id uuid NOT NULL UNIQUE,
    attempt_id uuid NOT NULL UNIQUE,
    worker_id text NOT NULL REFERENCES perfeng_control.native_worker_presence (worker_id),
    capability_sha256 bytea NOT NULL
        CHECK (octet_length(capability_sha256) = 32),
    issued_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    renew_after_seconds integer NOT NULL
        CHECK (renew_after_seconds BETWEEN 5 AND 300),
    plan jsonb NOT NULL,
    CHECK (expires_at > issued_at),
    CHECK (jsonb_typeof(plan) = 'object'),
    CHECK (plan ?& ARRAY['assignment', 'artifactPlan'])
);

CREATE INDEX native_execution_leases_worker_expiry
    ON perfeng_control.native_execution_leases (worker_id, expires_at);
