-- NULL is historical/custom absence, never a reconstruction or backfill request.
ALTER TABLE perfeng_control.integration_requests ADD COLUMN analysis_verification jsonb;
ALTER TABLE perfeng_control.integration_requests ADD CONSTRAINT integration_analysis_verification_shape
CHECK (analysis_verification IS NULL OR COALESCE(
    native_binding IS NOT NULL AND run_id IS NOT NULL
    AND jsonb_typeof(analysis_verification)='object'
    AND analysis_verification ?& ARRAY['policyBytes','producer','contractsVersion']
    AND (analysis_verification - ARRAY['policyBytes','producer','contractsVersion'])='{}'::jsonb
    AND jsonb_typeof(analysis_verification->'policyBytes')='string'
    AND jsonb_typeof(analysis_verification->'producer')='object'
    AND analysis_verification->>'contractsVersion' ~ '^[0-9]+\.[0-9]+\.[0-9]+$'
    AND octet_length(decode(analysis_verification->>'policyBytes','base64')) BETWEEN 1 AND 1048576
    AND encode(sha256(decode(analysis_verification->>'policyBytes','base64')),'hex')=native_binding->'policy'->>'sha256', false));

CREATE FUNCTION perfeng_control.guard_integration_analysis_verification() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP='TRUNCATE' THEN
        RAISE EXCEPTION 'analysis verification retention required' USING ERRCODE='55000';
    END IF;
    IF TG_OP='DELETE' THEN
        IF OLD.analysis_verification IS NOT NULL THEN
            RAISE EXCEPTION 'analysis verification retention required' USING ERRCODE='55000';
        END IF;
        RETURN OLD;
    END IF;
    IF OLD.analysis_verification IS DISTINCT FROM NEW.analysis_verification THEN
        RAISE EXCEPTION 'analysis verification is immutable' USING ERRCODE='55000';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER integration_analysis_verification_immutable BEFORE UPDATE OR DELETE
ON perfeng_control.integration_requests FOR EACH ROW
EXECUTE FUNCTION perfeng_control.guard_integration_analysis_verification();
CREATE TRIGGER integration_analysis_verification_no_truncate BEFORE TRUNCATE
ON perfeng_control.integration_requests FOR EACH STATEMENT
EXECUTE FUNCTION perfeng_control.guard_integration_analysis_verification();
