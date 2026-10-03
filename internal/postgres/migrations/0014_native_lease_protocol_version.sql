-- Preserve one Run authority across protocol generations while retaining
-- generation-specific worker referential integrity. Existing and older v1
-- writers use the default protocol version.
ALTER TABLE perfeng_control.native_execution_leases
    ADD COLUMN protocol_version smallint NOT NULL DEFAULT 1,
    ADD CONSTRAINT native_execution_leases_protocol_version
        CHECK (protocol_version IN (1, 2));

ALTER TABLE perfeng_control.native_execution_leases
    DROP CONSTRAINT native_execution_leases_worker_id_fkey,
    ADD COLUMN worker_v1_id text GENERATED ALWAYS AS (
        CASE WHEN protocol_version = 1 THEN worker_id END
    ) STORED,
    ADD COLUMN worker_v2_id text GENERATED ALWAYS AS (
        CASE WHEN protocol_version = 2 THEN worker_id END
    ) STORED,
    ADD CONSTRAINT native_execution_leases_worker_v1_fkey
        FOREIGN KEY (worker_v1_id)
        REFERENCES perfeng_control.native_worker_presence (worker_id),
    ADD CONSTRAINT native_execution_leases_worker_v2_fkey
        FOREIGN KEY (worker_v2_id)
        REFERENCES perfeng_control.native_worker_presence_v2 (worker_id);
