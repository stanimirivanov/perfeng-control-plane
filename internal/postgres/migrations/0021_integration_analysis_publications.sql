CREATE TABLE perfeng_control.integration_analysis_publications (
    principal text NOT NULL,
    issuer text NOT NULL,
    request_id text NOT NULL,
    source text NOT NULL CHECK (length(source) BETWEEN 1 AND 512),
    event_id text NOT NULL CHECK (length(event_id) BETWEEN 1 AND 256),
    artifact_id uuid NOT NULL REFERENCES perfeng_control.artifacts(artifact_id),
    payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 65536),
    digest text NOT NULL CHECK (digest = encode(sha256(payload), 'hex')),
    PRIMARY KEY (principal, issuer, request_id),
    UNIQUE (principal, source, event_id),
    FOREIGN KEY (principal, issuer, request_id)
        REFERENCES perfeng_control.integration_requests(principal, issuer, request_id)
);

CREATE FUNCTION perfeng_control.guard_integration_analysis_publication() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE parent perfeng_control.integration_requests%ROWTYPE; document jsonb; artifact_reference jsonb;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION 'analysis publication is immutable' USING ERRCODE = '55000';
    END IF;
    SELECT * INTO parent FROM perfeng_control.integration_requests
        WHERE principal=NEW.principal AND issuer=NEW.issuer AND request_id=NEW.request_id FOR SHARE;
    IF NOT FOUND OR parent.native_binding IS NULL THEN
        RAISE EXCEPTION 'native admission required' USING ERRCODE = '23514';
    END IF;
    document := convert_from(NEW.payload, 'UTF8')::jsonb;
    SELECT a.reference INTO artifact_reference FROM perfeng_control.artifacts a
        WHERE a.artifact_id=NEW.artifact_id AND a.run_id=parent.run_id FOR SHARE;
    IF NOT FOUND OR NOT COALESCE(
        document->>'specversion'='1.0' AND document->>'datacontenttype'='application/json'
        AND document->>'type'='dev.perfeng.integration.analysis-published.v1'
        AND document->>'dataschema'='https://perfeng.dev/schemas/performance-analysis/v1/performance-analysis.schema.json'
        AND document->>'source'=NEW.source AND document->>'id'=NEW.event_id
        AND NEW.source=parent.disposition->>'source'
        AND document->'data'->'context'=parent.context
        AND document->'data'->'run'=parent.disposition->'data'->'run'
        AND document->'data'->'native'=parent.native_binding
        AND artifact_reference->>'kind'='normalized' AND artifact_reference->>'format'='analysis-result/v1'
        AND document->'data'->'artifact'=jsonb_set(artifact_reference,'{kind}','"analysis"'::jsonb)
        AND (document->>'time')::timestamptz >= (parent.disposition->>'time')::timestamptz, false) THEN
        RAISE EXCEPTION 'analysis publication binding mismatch' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER integration_analysis_publication_immutable BEFORE INSERT OR UPDATE OR DELETE
ON perfeng_control.integration_analysis_publications FOR EACH ROW
EXECUTE FUNCTION perfeng_control.guard_integration_analysis_publication();
CREATE TRIGGER integration_analysis_publication_no_truncate BEFORE TRUNCATE
ON perfeng_control.integration_analysis_publications FOR EACH STATEMENT
EXECUTE FUNCTION perfeng_control.guard_integration_analysis_publication();
