-- Keep v2 reports outside the v1 presence table so older binaries and v1
-- scheduling never decode a v2 document as a v1 capability claim.
CREATE TABLE perfeng_control.native_worker_presence_v2 (
    worker_id text PRIMARY KEY
        CHECK (worker_id ~ '^[a-z][a-z0-9-]{1,62}$'),
    observed_at timestamptz NOT NULL,
    availability text NOT NULL
        CHECK (availability IN ('AVAILABLE', 'DRAINING', 'UNAVAILABLE')),
    qualified boolean NOT NULL,
    authorized_to_acquire boolean NOT NULL,
    CHECK (NOT authorized_to_acquire),
    presence jsonb NOT NULL,
    CHECK (jsonb_typeof(presence) = 'object'),
    CHECK (presence ?& ARRAY[
        'protocolVersion', 'agentVersion', 'availability',
        'capabilities', 'qualificationEvidence'
    ]),
    CHECK ((presence->>'protocolVersion')::integer = 2),
    CHECK (presence->>'availability' = availability),
    CHECK (NOT presence ? 'workerId'),
    CHECK (NOT presence ? 'qualified'),
    CHECK (NOT presence ? 'authorizedToAcquire')
);
