-- Inactive persistence foundation; no route, inferred history or execution activation.
CREATE TABLE perfeng_control.rerun_intents (
    actor_issuer perfeng_control.service_issuer NOT NULL,
    actor_id perfeng_control.service_subject NOT NULL,
    idempotency_key text NOT NULL CHECK (idempotency_key ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{15,127}$'),
    source_owner text NOT NULL,
    source_run_id text NOT NULL REFERENCES perfeng_control.run_services(run_id),
    report_id uuid NOT NULL,
    child_owner text NOT NULL,
    child_run_id text NOT NULL UNIQUE REFERENCES perfeng_control.run_services(run_id),
    receipt_id uuid NOT NULL UNIQUE,
    accepted_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at >= accepted_at + interval '24 hours'),
    receipt bytea NOT NULL CHECK (octet_length(receipt) BETWEEN 1 AND 65536),
    sha256 text NOT NULL CHECK (sha256 = encode(sha256(receipt), 'hex')),
    PRIMARY KEY (actor_issuer,actor_id,idempotency_key),
    FOREIGN KEY (source_owner,source_run_id) REFERENCES perfeng_control.report_comparison_inputs(principal,run_id),
    FOREIGN KEY (report_id,source_run_id) REFERENCES perfeng_control.artifacts(artifact_id,run_id),
    FOREIGN KEY (child_owner,child_run_id) REFERENCES perfeng_control.runs(principal,run_id),
    CHECK (child_run_id <> source_run_id)
);
CREATE TRIGGER rerun_intents_immutable BEFORE UPDATE OR DELETE ON perfeng_control.rerun_intents
    FOR EACH ROW EXECUTE FUNCTION perfeng_control.guard_service_evidence();
CREATE TRIGGER rerun_intents_no_truncate BEFORE TRUNCATE ON perfeng_control.rerun_intents
    EXECUTE FUNCTION perfeng_control.guard_service_evidence();
REVOKE ALL ON perfeng_control.rerun_intents FROM PUBLIC;
