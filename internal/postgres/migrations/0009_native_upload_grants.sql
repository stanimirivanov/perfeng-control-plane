-- Durable non-secret declarations for exact native artifact uploads. The
-- current lease row is replaceable, so historical grant fences intentionally
-- do not foreign-key lease_id or attempt_id to native_execution_leases.
CREATE TABLE perfeng_control.native_upload_grants (
    grant_id uuid PRIMARY KEY,
    run_id text NOT NULL REFERENCES perfeng_control.runs (run_id),
    lease_id uuid NOT NULL,
    attempt_id uuid NOT NULL,
    slot text NOT NULL
        CHECK (slot ~ '^[a-z][a-z0-9-]{1,62}$'),
    object_key text NOT NULL UNIQUE,
    declared_size bigint NOT NULL
        CHECK (declared_size BETWEEN 1 AND 536870912),
    maximum_bytes bigint NOT NULL
        CHECK (maximum_bytes BETWEEN declared_size AND 536870912),
    sha256 bytea NOT NULL
        CHECK (octet_length(sha256) = 32),
    media_type text NOT NULL
        CHECK (media_type ~ '^[a-z0-9.+-]+/[a-z0-9.+-]+$'),
    issued_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    UNIQUE (lease_id, slot),
    CHECK (expires_at > issued_at),
    CHECK (
        object_key = 'runs/' || run_id || '/attempts/' || attempt_id::text || '/' || slot
    )
);
