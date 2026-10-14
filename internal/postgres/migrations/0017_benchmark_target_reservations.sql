CREATE TABLE perfeng_control.benchmark_target_reservations (
    run_id text PRIMARY KEY REFERENCES perfeng_control.runs (run_id),
    target_id text NOT NULL UNIQUE CHECK (
        length(target_id) BETWEEN 1 AND 63 AND target_id ~ '^[a-z][a-z0-9]*(-[a-z0-9]+)*$'
    ),
    cluster_uid uuid NOT NULL,
    plan bytea NOT NULL CHECK (octet_length(plan) BETWEEN 1 AND 1048576),
    plan_sha256 text NOT NULL CHECK (
        plan_sha256 ~ '^[a-f0-9]{64}$' AND plan_sha256 = encode(sha256(plan), 'hex')
    ),
    admission bytea NOT NULL CHECK (octet_length(admission) BETWEEN 1 AND 1048576),
    namespace_uid uuid,
    request bytea CHECK (octet_length(request) BETWEEN 1 AND 1048576),
    created_at timestamptz NOT NULL,
    CONSTRAINT benchmark_request_requires_namespace CHECK (request IS NULL OR namespace_uid IS NOT NULL)
);
