-- Control-plane-owned schema; deliberately separate from prototype metadata.*.
CREATE TABLE perfeng_control.runs (
    run_id text PRIMARY KEY CHECK (run_id ~ '^perf-[0-9]{8}-[0-9]{6}-[a-f0-9]{8}$'),
    principal text NOT NULL CHECK (length(principal) > 0),
    snapshot jsonb NOT NULL,
    CHECK (jsonb_typeof(snapshot) = 'object'),
    CHECK (snapshot ?& ARRAY['id', 'state', 'revision', 'request', 'createdAt', 'updatedAt']),
    CHECK (snapshot->>'id' = run_id),
    CHECK ((snapshot->>'revision')::bigint BETWEEN 1 AND 9007199254740991),
    CHECK (snapshot->>'state' IN (
        'CREATED', 'VALIDATING', 'PROVISIONING', 'WARMING_UP', 'RUNNING',
        'COLLECTING', 'ANALYZING', 'REPORTING', 'CANCELLING', 'COMPLETED',
        'INVALID', 'ABORTED', 'INFRASTRUCTURE_FAILURE', 'TEST_FAILURE'
    )),
    UNIQUE (principal, run_id)
);
CREATE INDEX runs_state ON perfeng_control.runs ((snapshot->>'state'));

CREATE TABLE perfeng_control.create_bindings (
    principal text NOT NULL,
    idempotency_key text NOT NULL CHECK (
        idempotency_key ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{15,127}$'
    ),
    run_id text NOT NULL,
    original_snapshot jsonb NOT NULL,
    expires_at timestamptz NOT NULL,
    PRIMARY KEY (principal, idempotency_key),
    FOREIGN KEY (principal, run_id) REFERENCES perfeng_control.runs (principal, run_id),
    CHECK (original_snapshot->>'id' = run_id),
    CHECK (original_snapshot->>'state' = 'CREATED'),
    CHECK ((original_snapshot->>'revision')::bigint = 1)
);
CREATE INDEX create_bindings_expiry ON perfeng_control.create_bindings (expires_at);

CREATE TABLE perfeng_control.artifacts (
    artifact_id uuid PRIMARY KEY,
    run_id text NOT NULL REFERENCES perfeng_control.runs (run_id),
    reference jsonb NOT NULL,
    CHECK (jsonb_typeof(reference) = 'object'),
    CHECK (reference ?& ARRAY['id', 'runId', 'kind', 'uri', 'sha256', 'sizeBytes', 'mediaType', 'format']),
    CHECK ((reference->>'id')::uuid = artifact_id),
    CHECK (reference->>'runId' = run_id),
    CHECK (reference->>'kind' IN ('raw', 'normalized')),
    CHECK (reference->>'sha256' ~ '^[a-f0-9]{64}$'),
    CHECK ((reference->>'sizeBytes')::bigint >= 0)
);
CREATE INDEX artifacts_run ON perfeng_control.artifacts (run_id, artifact_id);
