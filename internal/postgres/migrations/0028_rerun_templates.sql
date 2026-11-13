-- Capture only reviewed future templates, never inferred historical Job bytes.
CREATE TABLE perfeng_control.rerun_templates (
    child_owner text NOT NULL,
    child_run_id text PRIMARY KEY REFERENCES perfeng_control.rerun_intents(child_run_id),
    payload bytea NOT NULL CHECK (octet_length(payload) BETWEEN 1 AND 1048576),
    sha256 text NOT NULL CHECK (sha256 = encode(sha256(payload), 'hex')),
    FOREIGN KEY (child_owner,child_run_id) REFERENCES perfeng_control.runs(principal,run_id)
);
CREATE FUNCTION perfeng_control.guard_rerun_template_capture() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog,pg_temp AS $$
DECLARE parent perfeng_control.runs%ROWTYPE;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION 'rerun template is immutable' USING ERRCODE = '55000';
    END IF;
    SELECT * INTO parent FROM perfeng_control.runs
        WHERE principal=NEW.child_owner AND run_id=NEW.child_run_id FOR UPDATE;
    IF NOT FOUND OR parent.snapshot->>'state'<>'VALIDATING'
        OR parent.execution_target<>'KUBERNETES'
        OR EXISTS(SELECT 1 FROM perfeng_control.kubernetes_executions WHERE run_id=NEW.child_run_id) THEN
        RAISE EXCEPTION 'template requires unbound validating child' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER rerun_template_capture BEFORE INSERT OR UPDATE OR DELETE ON perfeng_control.rerun_templates
    FOR EACH ROW EXECUTE FUNCTION perfeng_control.guard_rerun_template_capture();
CREATE TRIGGER rerun_templates_no_truncate BEFORE TRUNCATE ON perfeng_control.rerun_templates
    EXECUTE FUNCTION perfeng_control.guard_rerun_template_capture();
REVOKE ALL ON perfeng_control.rerun_templates FROM PUBLIC;
