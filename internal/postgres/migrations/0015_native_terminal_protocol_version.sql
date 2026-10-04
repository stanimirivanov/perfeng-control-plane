-- Preserve one terminal outcome per Run while binding each report to the
-- presence generation that issued its lease. Existing v1 reports and old v1
-- writers retain protocol version 1 through the default.
ALTER TABLE perfeng_control.native_terminal_reports
    ADD COLUMN protocol_version smallint NOT NULL DEFAULT 1,
    ADD CONSTRAINT native_terminal_reports_protocol_version
        CHECK (protocol_version IN (1, 2));

ALTER TABLE perfeng_control.native_terminal_reports
    DROP CONSTRAINT native_terminal_reports_worker_id_fkey,
    ADD COLUMN worker_v1_id text GENERATED ALWAYS AS (
        CASE WHEN protocol_version = 1 THEN worker_id END
    ) STORED,
    ADD COLUMN worker_v2_id text GENERATED ALWAYS AS (
        CASE WHEN protocol_version = 2 THEN worker_id END
    ) STORED,
    ADD CONSTRAINT native_terminal_reports_worker_v1_fkey
        FOREIGN KEY (worker_v1_id)
        REFERENCES perfeng_control.native_worker_presence (worker_id),
    ADD CONSTRAINT native_terminal_reports_worker_v2_fkey
        FOREIGN KEY (worker_v2_id)
        REFERENCES perfeng_control.native_worker_presence_v2 (worker_id);
