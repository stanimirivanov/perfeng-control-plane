-- Expand only: no ownership inference, automatic backfill or HTTP activation.
CREATE DOMAIN perfeng_control.service_issuer AS text
    CHECK (length(VALUE) BETWEEN 1 AND 512 AND VALUE ~ '^[A-Za-z][A-Za-z0-9+.-]*:' AND VALUE !~ '[[:space:]]');
CREATE DOMAIN perfeng_control.service_subject AS text
    CHECK (length(VALUE) BETWEEN 1 AND 256 AND VALUE ~ '[^[:space:]]');
CREATE DOMAIN perfeng_control.service_permission AS text CHECK (VALUE IN (
    'run:read', 'run:create', 'run:cancel', 'run:rerun',
    'baseline:create', 'baseline:qualify', 'baseline:approve', 'baseline:retire'
));

-- Only the schema owner provisions exact login/service assignments. SET ROLE
-- does not change session_user and cannot forge the accountable administrator.
CREATE TABLE perfeng_control.service_administrators (
    service_issuer perfeng_control.service_issuer NOT NULL,
    service_id perfeng_control.service_subject NOT NULL,
    administrator name NOT NULL,
    PRIMARY KEY (service_issuer, service_id, administrator)
);
CREATE TABLE perfeng_control.run_services (
    run_id text PRIMARY KEY,
    owner_principal text NOT NULL,
    service_issuer perfeng_control.service_issuer NOT NULL,
    service_id perfeng_control.service_subject NOT NULL,
    creator_issuer perfeng_control.service_issuer NOT NULL,
    creator_id perfeng_control.service_subject NOT NULL,
    creator_kind text NOT NULL CHECK (creator_kind IN ('HUMAN', 'CI')),
    review_ref text NOT NULL CHECK (length(review_ref) BETWEEN 1 AND 1024 AND review_ref ~ '[^[:space:]]'),
    FOREIGN KEY (owner_principal, run_id) REFERENCES perfeng_control.runs (principal, run_id)
);
CREATE TABLE perfeng_control.service_grants (
    service_issuer perfeng_control.service_issuer NOT NULL,
    service_id perfeng_control.service_subject NOT NULL,
    actor_issuer perfeng_control.service_issuer NOT NULL,
    actor_id perfeng_control.service_subject NOT NULL,
    permission perfeng_control.service_permission NOT NULL,
    enabled boolean NOT NULL,
    review_ref text NOT NULL CHECK (length(review_ref) BETWEEN 1 AND 1024 AND review_ref ~ '[^[:space:]]'),
    PRIMARY KEY (service_issuer, service_id, actor_issuer, actor_id, permission)
);
CREATE TABLE perfeng_control.service_access_audit (
    sequence bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    at timestamptz NOT NULL DEFAULT clock_timestamp(),
    administrator name NOT NULL DEFAULT session_user,
    operation text NOT NULL CHECK (operation IN ('BIND_RUN', 'GRANT', 'REVOKE')),
    service_issuer perfeng_control.service_issuer NOT NULL,
    service_id perfeng_control.service_subject NOT NULL,
    actor_issuer perfeng_control.service_issuer NOT NULL,
    actor_id perfeng_control.service_subject NOT NULL,
    run_id text,
    permission perfeng_control.service_permission,
    review_ref text NOT NULL,
    CHECK ((operation = 'BIND_RUN' AND run_id IS NOT NULL AND permission IS NULL)
        OR (operation IN ('GRANT', 'REVOKE') AND run_id IS NULL AND permission IS NOT NULL))
);

CREATE FUNCTION perfeng_control.guard_service_evidence() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    RAISE EXCEPTION 'immutable service evidence' USING ERRCODE = '23514';
END $$;
CREATE TRIGGER run_services_immutable BEFORE UPDATE OR DELETE ON perfeng_control.run_services
    FOR EACH ROW EXECUTE FUNCTION perfeng_control.guard_service_evidence();
CREATE TRIGGER run_services_no_truncate BEFORE TRUNCATE ON perfeng_control.run_services
    EXECUTE FUNCTION perfeng_control.guard_service_evidence();
CREATE TRIGGER service_audit_immutable BEFORE UPDATE OR DELETE ON perfeng_control.service_access_audit
    FOR EACH ROW EXECUTE FUNCTION perfeng_control.guard_service_evidence();
CREATE TRIGGER service_audit_no_truncate BEFORE TRUNCATE ON perfeng_control.service_access_audit
    EXECUTE FUNCTION perfeng_control.guard_service_evidence();

CREATE FUNCTION perfeng_control.audit_service_binding() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    PERFORM pg_catalog.set_config('synchronous_commit', 'on', true);
    INSERT INTO perfeng_control.service_access_audit
        (operation, service_issuer, service_id, actor_issuer, actor_id, run_id, review_ref)
    VALUES ('BIND_RUN', NEW.service_issuer, NEW.service_id, NEW.creator_issuer, NEW.creator_id, NEW.run_id, NEW.review_ref);
    RETURN NEW;
END $$;
CREATE TRIGGER run_services_audit AFTER INSERT ON perfeng_control.run_services
    FOR EACH ROW EXECUTE FUNCTION perfeng_control.audit_service_binding();

CREATE FUNCTION perfeng_control.audit_service_grant() RETURNS trigger
LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp AS $$
BEGIN
    PERFORM pg_catalog.set_config('synchronous_commit', 'on', true);
    INSERT INTO perfeng_control.service_access_audit
        (operation, service_issuer, service_id, actor_issuer, actor_id, permission, review_ref)
    VALUES (CASE WHEN NEW.enabled THEN 'GRANT' ELSE 'REVOKE' END,
        NEW.service_issuer, NEW.service_id, NEW.actor_issuer, NEW.actor_id, NEW.permission, NEW.review_ref);
    RETURN NEW;
END $$;
CREATE TRIGGER service_grants_audit AFTER INSERT OR UPDATE ON perfeng_control.service_grants
    FOR EACH ROW EXECUTE FUNCTION perfeng_control.audit_service_grant();

-- Explicit initial-operator path. Callers get EXECUTE, never table DML. The
-- definer is trusted, all relation names are qualified, and PUBLIC gets no entry.
CREATE FUNCTION perfeng_control.set_service_grant(
    service_issuer text, service_id text, actor_issuer text, actor_id text,
    permission text, enabled boolean, review_ref text
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, pg_temp
SET lock_timeout = '5s' AS $$
BEGIN
    PERFORM 1 FROM perfeng_control.service_administrators a
        WHERE a.service_issuer = $1 AND a.service_id = $2 AND a.administrator = session_user
        FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'service administration denied' USING ERRCODE = '42501';
    END IF;
    INSERT INTO perfeng_control.service_grants AS current_grant
        (service_issuer, service_id, actor_issuer, actor_id, permission, enabled, review_ref)
    VALUES ($1,$2,$3,$4,$5,$6,$7)
    ON CONFLICT ON CONSTRAINT service_grants_pkey DO UPDATE
        SET enabled = EXCLUDED.enabled, review_ref = EXCLUDED.review_ref
        WHERE current_grant.enabled IS DISTINCT FROM EXCLUDED.enabled;
END $$;
REVOKE ALL ON FUNCTION perfeng_control.set_service_grant(text,text,text,text,text,boolean,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION perfeng_control.guard_service_evidence() FROM PUBLIC;
REVOKE ALL ON FUNCTION perfeng_control.audit_service_binding() FROM PUBLIC;
REVOKE ALL ON FUNCTION perfeng_control.audit_service_grant() FROM PUBLIC;

REVOKE ALL ON perfeng_control.service_administrators, perfeng_control.run_services,
    perfeng_control.service_grants, perfeng_control.service_access_audit FROM PUBLIC;
