-- V2 acquisition is governed by the control-plane's explicit startup policy.
-- Keep the separate v2 presence and lease fencing; retire only the provisional
-- database-wide prohibition installed before the v2 HTTP lifecycle existed.
ALTER TABLE perfeng_control.native_worker_presence_v2
    DROP CONSTRAINT native_worker_presence_v2_authorized_to_acquire_check;
