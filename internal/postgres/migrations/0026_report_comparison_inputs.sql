-- Existing and old-binary-created Runs remain unenrolled; no historical backfill.
ALTER TABLE perfeng_control.runs ADD COLUMN report_capture_enrolled boolean NOT NULL DEFAULT false;
CREATE TABLE perfeng_control.report_comparison_inputs (
    principal text NOT NULL,
    run_id text NOT NULL,
    candidate_id uuid NOT NULL,
    payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 2097152),
    sha256 text NOT NULL CHECK (sha256 = encode(sha256(payload), 'hex')),
    PRIMARY KEY (principal, run_id),
    FOREIGN KEY (principal, run_id) REFERENCES perfeng_control.runs(principal, run_id),
    FOREIGN KEY (candidate_id, run_id) REFERENCES perfeng_control.artifacts(artifact_id, run_id)
);
CREATE FUNCTION perfeng_control.guard_report_comparison_inputs() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
DECLARE parent perfeng_control.runs%ROWTYPE; document jsonb; candidate jsonb; selected jsonb; approved jsonb;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION 'report comparison inputs are immutable' USING ERRCODE = '55000';
    END IF;
    SELECT * INTO parent FROM perfeng_control.runs WHERE principal=NEW.principal AND run_id=NEW.run_id FOR SHARE;
    IF NOT FOUND OR NOT parent.report_capture_enrolled OR parent.snapshot->>'state'<>'REPORTING' THEN
        RAISE EXCEPTION 'enrolled reporting Run required' USING ERRCODE = '23514';
    END IF;
    IF EXISTS (SELECT 1 FROM perfeng_control.artifacts
        WHERE run_id=NEW.run_id AND reference->>'format'='analysis-result/v1') THEN
        RAISE EXCEPTION 'cannot capture after report registration' USING ERRCODE = '23514';
    END IF;
    document := convert_from(NEW.payload,'UTF8')::jsonb;
    IF (document->>'schemaVersion')::integer IS DISTINCT FROM 1 THEN
        RAISE EXCEPTION 'unsupported report input format' USING ERRCODE = '23514';
    END IF;
    document := document->'inputs';
    SELECT reference INTO candidate FROM perfeng_control.artifacts WHERE artifact_id=NEW.candidate_id AND run_id=NEW.run_id FOR SHARE;
    IF NOT FOUND OR NOT COALESCE(document->'Request'=parent.snapshot->'request'
        AND document->'Candidate'=candidate AND candidate->>'format'='normalized-result/v1'
        AND encode(sha256(decode(document->>'PolicyBytes','base64')),'hex')=parent.snapshot->'request'->'policy'->>'sha256',false) THEN
        RAISE EXCEPTION 'report inputs differ from native bindings' USING ERRCODE = '23514';
    END IF;
    FOR selected IN SELECT value FROM jsonb_array_elements(COALESCE(NULLIF(document->'BaselineSnapshots','null'::jsonb),'[]'::jsonb))
        ORDER BY value->>'id',value->>'version' LOOP
        SELECT snapshot INTO approved FROM perfeng_control.baselines
            WHERE principal=NEW.principal AND baseline_id=selected->>'id' AND version=selected->>'version' FOR SHARE;
        IF NOT FOUND OR approved->>'state'<>'APPROVED' OR approved<>selected THEN
            RAISE EXCEPTION 'baseline approval changed before capture' USING ERRCODE = '23514';
        END IF;
    END LOOP;
    RETURN NEW;
END $$;
CREATE TRIGGER report_inputs_guard BEFORE INSERT OR UPDATE OR DELETE ON perfeng_control.report_comparison_inputs
    FOR EACH ROW EXECUTE FUNCTION perfeng_control.guard_report_comparison_inputs();
CREATE TRIGGER report_inputs_no_truncate BEFORE TRUNCATE ON perfeng_control.report_comparison_inputs
    EXECUTE FUNCTION perfeng_control.guard_report_comparison_inputs();

CREATE FUNCTION perfeng_control.guard_report_capture_enrollment() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    IF NEW.report_capture_enrolled IS DISTINCT FROM OLD.report_capture_enrolled THEN
        RAISE EXCEPTION 'report capture enrollment is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER report_capture_enrollment_immutable BEFORE UPDATE OF report_capture_enrolled ON perfeng_control.runs
    FOR EACH ROW EXECUTE FUNCTION perfeng_control.guard_report_capture_enrollment();

REVOKE ALL ON perfeng_control.report_comparison_inputs FROM PUBLIC;
