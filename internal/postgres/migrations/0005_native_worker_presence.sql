-- Last authenticated presence for independently provisioned native workers.
CREATE TABLE perfeng_control.native_worker_presence (
    worker_id text PRIMARY KEY
        CHECK (worker_id ~ '^[a-z][a-z0-9-]{1,62}$'),
    observed_at timestamptz NOT NULL,
    availability text NOT NULL
        CHECK (availability IN ('AVAILABLE', 'DRAINING', 'UNAVAILABLE')),
    qualified boolean NOT NULL,
    authorized_to_acquire boolean NOT NULL,
    presence jsonb NOT NULL,
    CHECK (jsonb_typeof(presence) = 'object'),
    CHECK (presence ?& ARRAY[
        'protocolVersion', 'agentVersion', 'availability',
        'capabilities', 'qualificationEvidence'
    ]),
    CHECK ((presence->>'protocolVersion')::integer = 1),
    CHECK (presence->>'availability' = availability),
    CHECK (NOT presence ? 'workerId'),
    CHECK (NOT presence ? 'qualified'),
    CHECK (NOT presence ? 'authorizedToAcquire')
);

CREATE INDEX native_worker_presence_eligibility
    ON perfeng_control.native_worker_presence (observed_at DESC)
    WHERE availability = 'AVAILABLE' AND qualified AND authorized_to_acquire;
