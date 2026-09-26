-- Durable idempotency marker for explicit native lease release. Nullable
-- columns preserve compatibility with active leases written by older binaries.
ALTER TABLE perfeng_control.native_execution_leases
    ADD COLUMN released_at timestamptz,
    ADD COLUMN release_reason text,
    ADD COLUMN release_message text,
    ADD CONSTRAINT native_execution_leases_release CHECK (
        (
            released_at IS NULL
            AND release_reason IS NULL
            AND release_message IS NULL
        )
        OR
        (
            released_at IS NOT NULL
            AND released_at >= issued_at
            AND expires_at <= released_at
            AND release_reason IN (
                'CANCELLATION_REQUESTED',
                'LEASE_LOST',
                'WORKER_SHUTDOWN',
                'PREPARATION_FAILED'
            )
            AND (
                release_message IS NULL
                OR char_length(release_message) BETWEEN 1 AND 512
            )
        )
    );
