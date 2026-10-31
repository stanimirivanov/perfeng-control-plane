-- Preserve existing execution identity; Linux never acquires a retained Windows Run.
ALTER TABLE perfeng_control.runs DROP CONSTRAINT runs_execution_target;
ALTER TABLE perfeng_control.runs ADD CONSTRAINT runs_execution_target CHECK (
    execution_target IN ('KUBERNETES', 'NATIVE_WINDOWS', 'NATIVE_LINUX')
);
