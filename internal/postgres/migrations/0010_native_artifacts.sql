-- Accepted native artifact identities. The upload grant owns the immutable
-- attempt, slot, object key and declared object facts; this table records only
-- the server-issued artifact identity, verified location and reviewed meaning.
CREATE TABLE perfeng_control.native_artifacts (
    artifact_id uuid PRIMARY KEY,
    grant_id uuid NOT NULL UNIQUE
        REFERENCES perfeng_control.native_upload_grants (grant_id),
    uri text NOT NULL
        CHECK (uri ~ '^s3://[a-z0-9][a-z0-9.-]*/runs/[^?#]+$'),
    kind text NOT NULL
        CHECK (kind ~ '^[a-z][a-z0-9-]{1,62}$'),
    format text NOT NULL
        CHECK (format ~ '^[a-z][a-z0-9-]{1,62}$'),
    registered_at timestamptz NOT NULL
);
