-- NULL preserves legacy/mixed-version absence; it is never inferred or backfilled.
ALTER TABLE perfeng_control.integration_requests ADD COLUMN native_binding jsonb;
ALTER TABLE perfeng_control.integration_requests ADD CONSTRAINT integration_native_binding_shape
CHECK (native_binding IS NULL OR COALESCE(
    run_id IS NOT NULL AND jsonb_typeof(native_binding) = 'object'
    AND native_binding ?& ARRAY['runId','testId','workload','policy','baseline']
    AND native_binding->>'runId' = run_id
    AND jsonb_typeof(native_binding->'workload') = 'object'
    AND jsonb_typeof(native_binding->'policy') = 'object'
    AND ((context->'baseline' = 'null'::jsonb AND native_binding->'baseline' = 'null'::jsonb)
         OR (jsonb_typeof(context->'baseline') = 'object'
             AND jsonb_typeof(native_binding->'baseline') = 'object')), false));

CREATE FUNCTION perfeng_control.guard_integration_native_binding() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'TRUNCATE' THEN
        RAISE EXCEPTION 'integration native binding retention required' USING ERRCODE = '55000';
    END IF;
    IF TG_OP = 'DELETE' THEN
        IF OLD.native_binding IS NOT NULL THEN
            RAISE EXCEPTION 'integration native binding retention required' USING ERRCODE = '55000';
        END IF;
        RETURN OLD;
    END IF;
    IF OLD.native_binding IS DISTINCT FROM NEW.native_binding
       OR (OLD.native_binding IS NOT NULL AND
           (OLD.principal,OLD.issuer,OLD.request_id,OLD.context,OLD.disposition,OLD.run_id)
           IS DISTINCT FROM
           (NEW.principal,NEW.issuer,NEW.request_id,NEW.context,NEW.disposition,NEW.run_id)) THEN
        RAISE EXCEPTION 'integration native binding is immutable' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER integration_native_binding_immutable BEFORE UPDATE OR DELETE
ON perfeng_control.integration_requests FOR EACH ROW
EXECUTE FUNCTION perfeng_control.guard_integration_native_binding();
CREATE TRIGGER integration_native_binding_no_truncate BEFORE TRUNCATE
ON perfeng_control.integration_requests FOR EACH STATEMENT
EXECUTE FUNCTION perfeng_control.guard_integration_native_binding();
