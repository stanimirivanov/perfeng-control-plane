-- First accepted terminal outcome for a native Run. The current lease row
-- retains capability ownership; this table preserves exact retry identity and
-- the resulting Run revision without storing bearer material.
CREATE TABLE perfeng_control.native_terminal_reports (
    run_id text PRIMARY KEY REFERENCES perfeng_control.runs (run_id),
    lease_id uuid NOT NULL UNIQUE,
    attempt_id uuid NOT NULL UNIQUE,
    worker_id text NOT NULL
        REFERENCES perfeng_control.native_worker_presence (worker_id),
    expected_run_revision bigint NOT NULL
        CHECK (expected_run_revision BETWEEN 1 AND 9007199254740991),
    kind text NOT NULL
        CHECK (kind IN ('COMPLETED', 'FAILED')),
    manifest_artifact_id uuid
        REFERENCES perfeng_control.native_artifacts (artifact_id),
    failure_code text,
    failure_message text,
    failure_retryable boolean,
    resulting_run_revision bigint NOT NULL
        CHECK (resulting_run_revision BETWEEN 1 AND 9007199254740991),
    accepted_at timestamptz NOT NULL,
    CONSTRAINT native_terminal_reports_shape CHECK (
        (
            kind = 'COMPLETED'
            AND manifest_artifact_id IS NOT NULL
            AND failure_code IS NULL
            AND failure_message IS NULL
            AND failure_retryable IS NULL
        )
        OR
        (
            kind = 'FAILED'
            AND manifest_artifact_id IS NULL
            AND failure_code IN (
                'PREPARATION_FAILED',
                'TOOL_FAILED',
                'CAPTURE_FAILED',
                'UPLOAD_FAILED',
                'HOST_BECAME_UNQUALIFIED'
            )
            AND char_length(failure_message) BETWEEN 1 AND 1024
            AND failure_retryable IS NOT NULL
        )
    )
);

