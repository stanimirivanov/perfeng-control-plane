-- Immutable cluster identity lets a new worker recover an already-created Job.
CREATE TABLE perfeng_control.kubernetes_executions (
    run_id text PRIMARY KEY REFERENCES perfeng_control.runs (run_id),
    namespace text NOT NULL CHECK (
        length(namespace) BETWEEN 1 AND 63
        AND namespace ~ '^[a-z0-9]([-a-z0-9]*[a-z0-9])?$'
    ),
    job_name text NOT NULL CHECK (job_name = run_id),
    job_uid text NOT NULL CHECK (
        length(job_uid) BETWEEN 1 AND 128
        AND job_uid ~ '^[a-zA-Z0-9][a-zA-Z0-9._:-]*$'
    ),
    spec_sha256 text NOT NULL CHECK (spec_sha256 ~ '^[a-f0-9]{64}$')
);
