-- ============================================================================
-- stayin_local — 002_seed_local.sql
-- Data operasional awal node kapal: cermin tenant, 12 kamar, 12 TV dengan kode
-- pairing, layout halaman awal, dan pengumuman dasar.
--
-- Film tidak di-seed. mst_movie_local diisi oleh mesin ingest (flashdisk) atau
-- oleh distribusi dari server utama.
-- Idempotent.
-- ============================================================================

-- ---------------------------------------------------------------------------
-- Cermin tenant
-- ---------------------------------------------------------------------------
INSERT INTO mst_tenants (id, slug, name, branding, license_quota, status)
VALUES (
    '11111111-1111-4111-8111-111111111111',
    'dlu-alfateve',
    'DLU X ALFATEVE',
    jsonb_build_object(
        'logo_url', '/api/v1/assets/Banners/logo-dlu-alfateve.png',
        'primary_color', '#0F4C81',
        'accent_color', '#F59E0B',
        'ship_name', 'KM Dharma Kartika',
        'support_note', 'Hiburan onboard dipersembahkan oleh DLU bersama AlfaTEVE'
    ),
    50,
    'ACTIVE'
)
ON CONFLICT (lower(slug)) DO NOTHING;

-- ---------------------------------------------------------------------------
-- Kamar
-- ---------------------------------------------------------------------------
INSERT INTO mst_rooms (tenant_id, room_number, deck, room_type, is_exclusive)
SELECT
    '11111111-1111-4111-8111-111111111111',
    v.room_number,
    v.deck,
    v.room_type,
    v.is_exclusive
FROM (VALUES
    ('101', 'Deck 1', 'STANDARD',  false),
    ('102', 'Deck 1', 'STANDARD',  false),
    ('103', 'Deck 1', 'STANDARD',  false),
    ('104', 'Deck 1', 'STANDARD',  false),
    ('105', 'Deck 2', 'STANDARD',  false),
    ('106', 'Deck 2', 'STANDARD',  false),
    ('107', 'Deck 2', 'STANDARD',  false),
    ('108', 'Deck 2', 'STANDARD',  false),
    ('VIP-01', 'Deck 3', 'VIP',       true),
    ('VIP-02', 'Deck 3', 'VIP',       true),
    ('VIP-03', 'Deck 3', 'EXCLUSIVE', true),
    ('VIP-04', 'Deck 3', 'EXCLUSIVE', true)
) AS v (room_number, deck, room_type, is_exclusive)
ON CONFLICT (tenant_id, room_number) DO NOTHING;

-- ---------------------------------------------------------------------------
-- TV: satu per kamar, pra-registrasi dengan kode pairing
-- ---------------------------------------------------------------------------
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
   AND r.room_number = v.room_number
ON CONFLICT (device_uid) DO NOTHING;

-- ---------------------------------------------------------------------------
-- Layout halaman awal (welcome screen saja)
-- ---------------------------------------------------------------------------
INSERT INTO mst_tv_layout_configs (tenant_id, name, config, version, is_active)
SELECT
    '11111111-1111-4111-8111-111111111111',
    'default',
    jsonb_build_object(
        'tenant_id', 'dlu-alfateve',
        'app_title', 'DLU X ALFATEVE',
        'welcome_screen', jsonb_build_object(
            'enabled', true,
            'layout_variant', 'SPLIT_LEFT_CARD',
            'background_image_url', '/api/v1/assets/Banners/welcome-bg.jpg',
            'logo_url', '/api/v1/assets/Banners/logo-dlu-alfateve.png',
            'primary_color', '#0F4C81',
            'accent_color', '#F59E0B',
            'cta_button_text', 'Masuk ke Hiburan',
            'default_greeting', 'Selamat berlayar{guest_suffix}. Nikmati hiburan pilihan di kabin {room_number}.',
            'show_room_info', true,
            'show_clock', true,
            'overscan_percent', 4
        )
    ),
    1,
    true
WHERE NOT EXISTS (
    SELECT 1 FROM mst_tv_layout_configs
    WHERE tenant_id = '11111111-1111-4111-8111-111111111111'
);

-- ---------------------------------------------------------------------------
-- Pengumuman
-- ---------------------------------------------------------------------------
INSERT INTO mst_announcements (tenant_id, message, severity, is_active)
SELECT
    '11111111-1111-4111-8111-111111111111',
    v.message,
    v.severity,
    true
FROM (VALUES
    ('Selamat datang di KM Dharma Kartika. Hiburan onboard siap menemani perjalanan Anda.', 'INFO'),
    ('Informasi keselamatan: lokasi sekoci dan muster station tertera pada pintu kabin Anda.', 'WARNING')
) AS v (message, severity)
WHERE NOT EXISTS (
    SELECT 1 FROM mst_announcements
    WHERE tenant_id = '11111111-1111-4111-8111-111111111111'
);
