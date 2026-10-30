CREATE TABLE perfeng_control.integration_publication_events (
    principal text NOT NULL,
    issuer text NOT NULL,
    request_id text NOT NULL,
    source text NOT NULL CHECK (length(source) BETWEEN 1 AND 512),
    principal_issuer text NOT NULL CHECK (length(principal_issuer) BETWEEN 1 AND 512),
    report_id uuid NOT NULL REFERENCES perfeng_control.artifacts(artifact_id),
    candidate_id uuid NOT NULL REFERENCES perfeng_control.artifacts(artifact_id),
    event_id text NOT NULL CHECK (length(event_id) BETWEEN 1 AND 256),
    published_at timestamptz NOT NULL CHECK (isfinite(published_at)),
    PRIMARY KEY (principal, issuer, request_id),
    UNIQUE (principal, source, event_id),
    CHECK (report_id <> candidate_id),
    FOREIGN KEY (principal, issuer, request_id)
        REFERENCES perfeng_control.integration_requests(principal, issuer, request_id)
);

CREATE FUNCTION perfeng_control.guard_integration_publication_event() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE parent perfeng_control.integration_requests%ROWTYPE; report jsonb; candidate jsonb;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION 'publication event reservation is immutable' USING ERRCODE='55000';
    END IF;
    SELECT * INTO parent FROM perfeng_control.integration_requests
        WHERE principal=NEW.principal AND issuer=NEW.issuer AND request_id=NEW.request_id FOR SHARE;
    IF NOT FOUND OR parent.native_binding IS NULL OR parent.analysis_verification IS NULL THEN
        RAISE EXCEPTION 'reviewed native admission required' USING ERRCODE='23514';
    END IF;
    SELECT reference INTO report FROM perfeng_control.artifacts
        WHERE artifact_id=NEW.report_id AND run_id=parent.run_id FOR SHARE;
    SELECT reference INTO candidate FROM perfeng_control.artifacts
        WHERE artifact_id=NEW.candidate_id AND run_id=parent.run_id FOR SHARE;
    IF NOT COALESCE(NEW.source=parent.disposition->>'source'
        AND NEW.principal_issuer=parent.context->'principal'->>'issuer'
        AND NEW.published_at >= (parent.disposition->>'time')::timestamptz
        AND report->>'kind'='normalized' AND report->>'format'='analysis-result/v1'
        AND candidate->>'kind'='normalized' AND candidate->>'format'='normalized-result/v1'
        AND report->>'mediaType'='application/json' AND candidate->>'mediaType'='application/json'
        AND (report->>'sizeBytes')::bigint BETWEEN 1 AND 1048576
        AND (candidate->>'sizeBytes')::bigint > 0 AND report->>'uri'<>candidate->>'uri', false) THEN
        RAISE EXCEPTION 'publication event evidence mismatch' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER integration_publication_event_immutable BEFORE INSERT OR UPDATE OR DELETE
ON perfeng_control.integration_publication_events FOR EACH ROW
EXECUTE FUNCTION perfeng_control.guard_integration_publication_event();
CREATE TRIGGER integration_publication_event_no_truncate BEFORE TRUNCATE
ON perfeng_control.integration_publication_events FOR EACH STATEMENT
EXECUTE FUNCTION perfeng_control.guard_integration_publication_event();
