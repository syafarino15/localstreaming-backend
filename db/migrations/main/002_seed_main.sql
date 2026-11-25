-- ============================================================================
-- stayin_main — 002_seed_main.sql
-- Data awal sisi utama: tenant DLU, node kapal terdaftar, konfigurasi aplikasi
-- dan feature flag. Katalog film TIDAK di-seed di sini; film masuk lewat CMS
-- atau dilaporkan oleh node lokal.
-- Idempotent.
-- ============================================================================

-- ---------------------------------------------------------------------------
-- Tenant
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
-- Node lokal yang terdaftar
-- ---------------------------------------------------------------------------
INSERT INTO mst_edge_nodes (id, tenant_id, node_code, name, base_url, tunnel_ip, status, agent_version)
VALUES (
    '22222222-2222-4222-8222-222222222222',
    '11111111-1111-4111-8111-111111111111',
    'DLU-NODE-01',
    'Local Server KM Dharma Kartika',
    'http://192.168.1.10:8080',
    '',
    'UNKNOWN',
    '1.0.0'
)
ON CONFLICT (tenant_id, node_code) DO NOTHING;

-- ---------------------------------------------------------------------------
-- Konfigurasi aplikasi dan feature flag
-- ---------------------------------------------------------------------------
INSERT INTO mst_app_configs (tenant_id)
SELECT id FROM mst_tenants WHERE lower(slug) = 'dlu-alfateve'
ON CONFLICT (tenant_id) DO NOTHING;

INSERT INTO mst_feature_flags (tenant_id, flag_key, enabled, note)
SELECT t.id, v.flag_key, v.enabled, v.note
FROM mst_tenants t
CROSS JOIN (VALUES
    ('usb_playback',     true,  'Pemutaran film langsung dari flashdisk yang dicolok ke TV'),
    ('pwa_remote',       false, 'Remote D-Pad lewat ponsel penumpang'),
    ('announcement_bar', true,  'Running text pengumuman di bawah layar'),
    ('safety_video',     false, 'Video keselamatan wajib sebelum hiburan'),
    ('apk_auto_update',  false, 'Pembaruan APK otomatis dari server lokal')
) AS v (flag_key, enabled, note)
WHERE lower(t.slug) = 'dlu-alfateve'
ON CONFLICT (tenant_id, flag_key) DO NOTHING;
