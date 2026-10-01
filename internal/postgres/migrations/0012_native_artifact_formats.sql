-- Align persisted artifact formats with the pinned Native Worker API format
-- shape. Existing values remain valid; canonical formats include a slash.
ALTER TABLE perfeng_control.native_artifacts
    DROP CONSTRAINT native_artifacts_format_check;

ALTER TABLE perfeng_control.native_artifacts
    ADD CONSTRAINT native_artifacts_format_check
    CHECK (format ~ '^[a-z0-9][a-z0-9._/-]*$');
