ALTER TABLE perfeng_control.runs
    ADD COLUMN execution_target text;

UPDATE perfeng_control.runs
SET execution_target = 'KUBERNETES';

ALTER TABLE perfeng_control.runs
    ALTER COLUMN execution_target SET NOT NULL,
    ADD CONSTRAINT runs_execution_target CHECK (
        execution_target IN ('KUBERNETES', 'NATIVE_WINDOWS')
    );

CREATE INDEX runs_execution_target_state
    ON perfeng_control.runs (execution_target, (snapshot->>'state'));
