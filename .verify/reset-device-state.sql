-- ============================================================================
-- reset-device-state.sql
--
-- Development helper for stayin_local. Verification runs pair real devices,
-- which consumes the seeded pairing codes and renames UNASSIGNED-<room> rows to
-- the test device_uid. This restores the 12 pre-registered TVs and clears guest
-- data so full-check.mjs can run again from a clean state.
--
-- It only touches operational rows in stayin_local. Catalog, rooms, layout and
-- announcements are left alone.
--
-- Usage:
--   psql -h 127.0.0.1 -p 5433 -U stayin -d stayin_local -f .verify/reset-device-state.sql
-- ============================================================================

BEGIN;

-- Pull every TV out of the way: they are re-created below.
DELETE FROM trn_remote_commands;
DELETE FROM trn_watch_logs;
DELETE FROM mst_tv_devices;

-- Guests belong to a stay; clear them so the greeting assertion starts blank.
UPDATE mst_rooms
SET guest_name = '',
    guest_salutation = '',
    guest_checkin_at = NULL,
    guest_checkout_at = NULL
WHERE tenant_id = '11111111-1111-4111-8111-111111111111';

INSERT INTO mst_tv_devices (tenant_id, room_id, device_uid, label, pairing_code, pairing_expires_at, status)
SELECT
    r.tenant_id,
    r.id,
    'UNASSIGNED-' || r.room_number,
    'TV Kamar ' || r.room_number,
    v.pairing_code,
    now() + interval '365 days',
    'UNPAIRED'
FROM (VALUES
    ('101',    '101001'),
    ('102',    '101002'),
    ('103',    '101003'),
    ('104',    '101004'),
    ('105',    '101005'),
    ('106',    '101006'),
    ('107',    '101007'),
    ('108',    '101008'),
    ('VIP-01', '201001'),
    ('VIP-02', '201002'),
    ('VIP-03', '201003'),
    ('VIP-04', '201004')
) AS v (room_number, pairing_code)
JOIN mst_rooms r
    ON r.tenant_id = '11111111-1111-4111-8111-111111111111'
   AND r.room_number = v.room_number;

COMMIT;
