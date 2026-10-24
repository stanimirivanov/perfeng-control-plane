-- Permanent correlation outlives native 24-hour create-key bindings.
CREATE TABLE perfeng_control.integration_requests (
    principal text NOT NULL CHECK (length(principal) > 0),
    issuer text NOT NULL CHECK (length(issuer) BETWEEN 1 AND 512),
    request_id text NOT NULL CHECK (length(request_id) BETWEEN 1 AND 256),
    attempt_issuer text NOT NULL CHECK (length(attempt_issuer) BETWEEN 1 AND 512),
    attempt_id text NOT NULL CHECK (length(attempt_id) BETWEEN 1 AND 256),
    context jsonb NOT NULL CHECK (jsonb_typeof(context) = 'object'),
    disposition jsonb NOT NULL CHECK (jsonb_typeof(disposition) = 'object'),
    run_id text,
    PRIMARY KEY (principal, issuer, request_id),
    UNIQUE (principal, attempt_issuer, attempt_id),
    UNIQUE (principal, run_id),
    FOREIGN KEY (principal, run_id) REFERENCES perfeng_control.runs(principal, run_id),
    CHECK (context->'request'->>'issuer' IS NOT DISTINCT FROM issuer AND context->'request'->>'id' IS NOT DISTINCT FROM request_id),
    CHECK (context->'attempt'->>'issuer' IS NOT DISTINCT FROM attempt_issuer AND context->'attempt'->>'id' IS NOT DISTINCT FROM attempt_id),
    CHECK (context->'principal'->>'id' IS NOT DISTINCT FROM principal),
    CHECK (disposition->'data'->'context' IS NOT DISTINCT FROM context),
    CHECK (COALESCE((run_id IS NULL AND disposition->>'type' = 'dev.perfeng.integration.rejected.v1'
                    AND jsonb_typeof(disposition->'data'->'error') = 'object') OR
           (run_id IS NOT NULL AND disposition->>'type' = 'dev.perfeng.integration.accepted.v1'
            AND disposition->'data'->'run'->>'id' = run_id
            AND disposition->'data'->'run'->>'issuer' = disposition->>'source'), false))
);
CREATE TABLE perfeng_control.integration_events (
    principal text NOT NULL,
    source text NOT NULL CHECK (length(source) BETWEEN 1 AND 512),
    event_id text NOT NULL CHECK (length(event_id) BETWEEN 1 AND 256),
    digest text NOT NULL CHECK (digest ~ '^[0-9a-f]{64}$'),
    issuer text NOT NULL,
    request_id text NOT NULL,
    PRIMARY KEY (principal, source, event_id),
    FOREIGN KEY (principal, issuer, request_id)
        REFERENCES perfeng_control.integration_requests(principal, issuer, request_id)
);
