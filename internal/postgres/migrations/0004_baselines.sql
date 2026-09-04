-- Principal-scoped, versioned baseline snapshots backed by completed Run evidence.
ALTER TABLE perfeng_control.artifacts
    ADD CONSTRAINT artifacts_identity_run_unique UNIQUE (artifact_id, run_id);

CREATE TABLE perfeng_control.baselines (
    principal text NOT NULL CHECK (length(principal) > 0),
    baseline_id text NOT NULL CHECK (baseline_id ~ '^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$'),
    version text NOT NULL CHECK (version ~ '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'),
    source_run_id text NOT NULL,
    artifact_id uuid NOT NULL,
    revision bigint NOT NULL CHECK (revision BETWEEN 1 AND 9007199254740991),
    state text NOT NULL CHECK (state IN ('CANDIDATE', 'QUALIFIED', 'APPROVED', 'RETIRED')),
    snapshot jsonb NOT NULL,
    PRIMARY KEY (principal, baseline_id, version),
    FOREIGN KEY (principal, source_run_id)
        REFERENCES perfeng_control.runs (principal, run_id),
    FOREIGN KEY (artifact_id, source_run_id)
        REFERENCES perfeng_control.artifacts (artifact_id, run_id),
    CHECK (jsonb_typeof(snapshot) = 'object'),
    CHECK (snapshot ?& ARRAY[
        'schemaVersion', 'kind', 'id', 'version', 'revision', 'state', 'testId',
        'sourceRunId', 'artifact', 'software', 'workload', 'environment', 'dataset',
        'qualification', 'createdAt', 'lifecycle'
    ]),
    CHECK (jsonb_typeof(snapshot->'revision') = 'number'),
    CHECK (jsonb_typeof(snapshot->'artifact') = 'object'),
    CHECK (jsonb_typeof(snapshot->'qualification') = 'object'),
    CHECK (jsonb_typeof(snapshot->'lifecycle') = 'array'),
    CHECK ((snapshot->>'schemaVersion')::integer = 1),
    CHECK (snapshot->>'kind' = 'PerformanceBaseline'),
    CHECK (snapshot->>'id' = baseline_id),
    CHECK (snapshot->>'version' = version),
    CHECK ((snapshot->>'revision')::bigint = revision),
    CHECK (snapshot->>'state' = state),
    CHECK (snapshot->>'sourceRunId' = source_run_id),
    CHECK ((snapshot->'artifact'->>'id')::uuid = artifact_id),
    CHECK (snapshot->'artifact'->>'runId' = source_run_id),
    CHECK (jsonb_array_length(snapshot->'lifecycle') = revision),
    CHECK (snapshot->'lifecycle'->0->>'state' = 'CANDIDATE'),
    CHECK (snapshot->'lifecycle'->0->>'at' = snapshot->>'createdAt'),
    CHECK (snapshot->'lifecycle'->-1->>'state' = state),
    CHECK (
        (state = 'CANDIDATE' AND snapshot->'qualification'->>'status' <> 'PASSED')
        OR state = 'RETIRED'
        OR (state IN ('QUALIFIED', 'APPROVED') AND snapshot->'qualification'->>'status' = 'PASSED')
    )
);

CREATE INDEX baselines_approved
    ON perfeng_control.baselines (principal, baseline_id, version)
    WHERE state = 'APPROVED';
