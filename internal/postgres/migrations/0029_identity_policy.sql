-- Expand only. No inferred enrollment, default caller or serving activation.
CREATE TABLE perfeng_control.identity_enrollments (
    issuer perfeng_control.service_issuer NOT NULL,
    actor_id perfeng_control.service_subject NOT NULL,
    actor_kind text NOT NULL CHECK (actor_kind IN ('HUMAN','CI')),
    enabled boolean NOT NULL,
    review_ref text NOT NULL CHECK (length(review_ref) BETWEEN 1 AND 1024 AND review_ref ~ '[^[:space:]]'),
    PRIMARY KEY (issuer,actor_id)
);
CREATE TABLE perfeng_control.identity_delegations (
    issuer perfeng_control.service_issuer NOT NULL,
    actor_id perfeng_control.service_subject NOT NULL,
    caller_id perfeng_control.service_subject NOT NULL CHECK (caller_id ~ '^client:.+'),
    enabled boolean NOT NULL,
    review_ref text NOT NULL CHECK (length(review_ref) BETWEEN 1 AND 1024 AND review_ref ~ '[^[:space:]]'),
    PRIMARY KEY (issuer,actor_id,caller_id),
    FOREIGN KEY (issuer,actor_id) REFERENCES perfeng_control.identity_enrollments(issuer,actor_id)
);
CREATE TABLE perfeng_control.identity_policy_audit (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at timestamptz NOT NULL DEFAULT clock_timestamp(),
    administrator name NOT NULL DEFAULT session_user,
    issuer perfeng_control.service_issuer NOT NULL,
    actor_id perfeng_control.service_subject NOT NULL,
    actor_kind text NOT NULL CHECK (actor_kind IN ('HUMAN','CI')),
    caller_id perfeng_control.service_subject,
    enabled boolean NOT NULL,
    review_ref text NOT NULL
);

-- Provisioning is a trusted schema-owner operation, never a runtime permission.
-- Preserve identity meaning and history: revoke with enabled=false, not deletion.
CREATE FUNCTION perfeng_control.audit_identity_policy() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog,pg_temp AS $$
DECLARE kind text;
DECLARE caller text;
BEGIN
    IF TG_OP = 'DELETE' OR TG_OP = 'TRUNCATE' THEN
        RAISE EXCEPTION 'identity policy must be disabled, not removed' USING ERRCODE = '23514';
    END IF;
    IF TG_OP = 'UPDATE' THEN
        IF (NEW.issuer,NEW.actor_id) IS DISTINCT FROM (OLD.issuer,OLD.actor_id) THEN
            RAISE EXCEPTION 'immutable identity binding' USING ERRCODE = '23514';
        END IF;
        IF TG_TABLE_NAME = 'identity_enrollments' THEN
            IF NEW.actor_kind IS DISTINCT FROM OLD.actor_kind THEN
                RAISE EXCEPTION 'immutable identity kind' USING ERRCODE = '23514';
            END IF;
        ELSE
            IF NEW.caller_id IS DISTINCT FROM OLD.caller_id THEN
                RAISE EXCEPTION 'immutable caller binding' USING ERRCODE = '23514';
            END IF;
        END IF;
    END IF;
    IF TG_TABLE_NAME = 'identity_enrollments' THEN
        kind := NEW.actor_kind;
    ELSE
        caller := NEW.caller_id;
        SELECT e.actor_kind INTO STRICT kind FROM perfeng_control.identity_enrollments e
            WHERE e.issuer=NEW.issuer AND e.actor_id=NEW.actor_id;
    END IF;
    PERFORM pg_catalog.set_config('synchronous_commit','on',true);
    INSERT INTO perfeng_control.identity_policy_audit
        (issuer,actor_id,actor_kind,caller_id,enabled,review_ref)
        VALUES (NEW.issuer,NEW.actor_id,kind,caller,NEW.enabled,NEW.review_ref);
    RETURN NEW;
END $$;
CREATE TRIGGER identity_enrollment_audit AFTER INSERT OR UPDATE OR DELETE
    ON perfeng_control.identity_enrollments FOR EACH ROW
    EXECUTE FUNCTION perfeng_control.audit_identity_policy();
CREATE TRIGGER identity_delegation_audit AFTER INSERT OR UPDATE OR DELETE
    ON perfeng_control.identity_delegations FOR EACH ROW
    EXECUTE FUNCTION perfeng_control.audit_identity_policy();
CREATE TRIGGER identity_enrollment_no_truncate BEFORE TRUNCATE
    ON perfeng_control.identity_enrollments EXECUTE FUNCTION perfeng_control.audit_identity_policy();
CREATE TRIGGER identity_delegation_no_truncate BEFORE TRUNCATE
    ON perfeng_control.identity_delegations EXECUTE FUNCTION perfeng_control.audit_identity_policy();
CREATE TRIGGER identity_audit_immutable BEFORE UPDATE OR DELETE
    ON perfeng_control.identity_policy_audit FOR EACH ROW
    EXECUTE FUNCTION perfeng_control.guard_service_evidence();
CREATE TRIGGER identity_audit_no_truncate BEFORE TRUNCATE
    ON perfeng_control.identity_policy_audit EXECUTE FUNCTION perfeng_control.guard_service_evidence();
REVOKE ALL ON FUNCTION perfeng_control.audit_identity_policy() FROM PUBLIC;
REVOKE ALL ON perfeng_control.identity_enrollments, perfeng_control.identity_delegations,
    perfeng_control.identity_policy_audit FROM PUBLIC;
