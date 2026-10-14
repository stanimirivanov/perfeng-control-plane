ALTER TABLE perfeng_control.benchmark_target_reservations
    ADD COLUMN cleanup_started_at timestamptz,
    ADD COLUMN cleanup_fenced_at timestamptz,
    ADD COLUMN cleanup_not_before timestamptz,
    ADD COLUMN cleanup_change jsonb,
    ADD COLUMN cleanup_receipt bytea,
    ADD COLUMN released_at timestamptz,
    ADD CONSTRAINT benchmark_cleanup_shape CHECK (
        (cleanup_started_at IS NULL AND cleanup_fenced_at IS NULL AND cleanup_not_before IS NULL AND cleanup_change IS NULL
            AND cleanup_receipt IS NULL AND released_at IS NULL)
        OR (cleanup_started_at IS NOT NULL AND cleanup_not_before IS NOT NULL
            AND cleanup_not_before >= cleanup_started_at + interval '2 minutes'
            AND cleanup_change IS NOT NULL AND jsonb_typeof(cleanup_change) = 'object'
            AND (cleanup_fenced_at IS NULL OR (cleanup_fenced_at >= cleanup_started_at
                AND cleanup_not_before >= cleanup_fenced_at + interval '2 minutes'))
            AND ((released_at IS NULL AND cleanup_receipt IS NULL)
                OR (released_at IS NOT NULL AND cleanup_fenced_at IS NOT NULL AND released_at >= cleanup_not_before
                    AND cleanup_receipt IS NOT NULL
                    AND octet_length(cleanup_receipt) BETWEEN 1 AND 1048576)))
    );

ALTER TABLE perfeng_control.benchmark_target_reservations
    DROP CONSTRAINT benchmark_target_reservations_target_id_key;

CREATE UNIQUE INDEX benchmark_active_target
    ON perfeng_control.benchmark_target_reservations (target_id)
    WHERE released_at IS NULL;
