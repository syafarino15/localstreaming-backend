-- ============================================================================
-- stayin_local — SERVER LOKAL (aplikasi yang berjalan di kapal / mini PC)
-- 001_local_schema.sql
--
-- Database ini adalah aplikasi kedua, terpisah dari stayin_main. Isinya hanya
-- data operasional yang dibutuhkan saat kapal kehilangan internet:
--   - cermin katalog film yang sudah didistribusikan ke node ini
--   - mst_movie_local: pemetaan berkas fisik nyata ke URL streaming
--   - kamar, TV, layout halaman awal, pengumuman, tamu
--   - progres tonton, perintah remote, riwayat scan, audit lokal
--
-- Tidak ada tabel akun admin, tenant master, rilis APK maupun antrean
-- distribusi di sini; semuanya milik stayin_main.
-- ============================================================================

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ---------------------------------------------------------------------------
-- mst_tenants — cermin identitas tenant (branding dipakai layar welcome)
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mst_tenants (
    id              uuid PRIMARY KEY,
    slug            text        NOT NULL,
    name            text        NOT NULL,
    branding        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    license_quota   integer     NOT NULL DEFAULT 50,
    status          text        NOT NULL DEFAULT 'ACTIVE',
    catalog_version integer     NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_tenants_status CHECK (status IN ('ACTIVE', 'SUSPENDED'))
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_tenants_slug ON mst_tenants (lower(slug));

DROP TRIGGER IF EXISTS trg_tenants_updated_at ON mst_tenants;
CREATE TRIGGER trg_tenants_updated_at
    BEFORE UPDATE ON mst_tenants
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- mst_rooms — kabin dan personalisasi tamu VIP
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mst_rooms (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid        NOT NULL REFERENCES mst_tenants (id) ON DELETE CASCADE,
    room_number      text        NOT NULL,
    deck             text        NOT NULL DEFAULT '',
    room_type        text        NOT NULL DEFAULT 'STANDARD',
    is_exclusive     boolean     NOT NULL DEFAULT false,
    guest_name       text        NOT NULL DEFAULT '',
    guest_salutation text        NOT NULL DEFAULT '',
    guest_checkin_at timestamptz,
    guest_checkout_at timestamptz,
    status           text        NOT NULL DEFAULT 'ACTIVE',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_rooms_type CHECK (room_type IN ('STANDARD', 'EXCLUSIVE', 'VIP')),
    CONSTRAINT chk_rooms_status CHECK (status IN ('ACTIVE', 'MAINTENANCE')),
    CONSTRAINT uq_rooms_number UNIQUE (tenant_id, room_number)
);

CREATE INDEX IF NOT EXISTS idx_rooms_tenant ON mst_rooms (tenant_id);

DROP TRIGGER IF EXISTS trg_rooms_updated_at ON mst_rooms;
CREATE TRIGGER trg_rooms_updated_at
    BEFORE UPDATE ON mst_rooms
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- mst_tv_devices — TV dan set-top box di kapal
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mst_tv_devices (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id          uuid        NOT NULL REFERENCES mst_tenants (id) ON DELETE CASCADE,
    room_id            uuid REFERENCES mst_rooms (id) ON DELETE SET NULL,
    device_uid         text        NOT NULL,
    label              text        NOT NULL DEFAULT '',
    model              text        NOT NULL DEFAULT '',
    app_version        text        NOT NULL DEFAULT '',
    pairing_code       text,
    pairing_expires_at timestamptz,
    device_token_hash  text        NOT NULL DEFAULT '',
    status             text        NOT NULL DEFAULT 'UNPAIRED',
    ip_address         text        NOT NULL DEFAULT '',
    last_seen_at       timestamptz,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_devices_status CHECK (status IN ('UNPAIRED', 'ONLINE', 'OFFLINE', 'DISABLED')),
    CONSTRAINT uq_devices_uid UNIQUE (device_uid),
    CONSTRAINT uq_devices_pairing UNIQUE (pairing_code)
);

CREATE INDEX IF NOT EXISTS idx_devices_tenant_status ON mst_tv_devices (tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_devices_room ON mst_tv_devices (room_id);

DROP TRIGGER IF EXISTS trg_devices_updated_at ON mst_tv_devices;
CREATE TRIGGER trg_devices_updated_at
    BEFORE UPDATE ON mst_tv_devices
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- mst_tv_layout_configs — dokumen layout HALAMAN AWAL (welcome screen)
--
-- Layout ini hanya mengatur tampilan pembuka per tenant. Komposisi katalog
-- (urutan baris, film unggulan) bukan bagian dari dokumen ini.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mst_tv_layout_configs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid        NOT NULL REFERENCES mst_tenants (id) ON DELETE CASCADE,
    name       text        NOT NULL DEFAULT 'default',
    config     jsonb       NOT NULL,
    version    integer     NOT NULL DEFAULT 1,
    is_active  boolean     NOT NULL DEFAULT false,
    updated_by text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_layout_active
    ON mst_tv_layout_configs (tenant_id)
    WHERE is_active;

CREATE INDEX IF NOT EXISTS idx_layout_tenant ON mst_tv_layout_configs (tenant_id, version DESC);

DROP TRIGGER IF EXISTS trg_layout_updated_at ON mst_tv_layout_configs;
CREATE TRIGGER trg_layout_updated_at
    BEFORE UPDATE ON mst_tv_layout_configs
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- mst_movies — cermin katalog yang sudah sampai di node ini
--
-- id sengaja sama dengan mst_movies.id di stayin_main sehingga distribusi
-- bersifat idempoten. Tidak ada kolom status authoring di sini; film ada di
-- tabel ini berarti sudah didistribusikan ke kapal.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mst_movies (
    id               uuid PRIMARY KEY,
    tenant_id        uuid        NOT NULL REFERENCES mst_tenants (id) ON DELETE CASCADE,
    slug             text        NOT NULL,
    title            text        NOT NULL,
    year             integer     NOT NULL DEFAULT 0,
    duration_seconds integer     NOT NULL DEFAULT 0,
    genres           text[]      NOT NULL DEFAULT '{}',
    poster_path      text        NOT NULL DEFAULT '',
    banner_path      text        NOT NULL DEFAULT '',
    is_featured      boolean     NOT NULL DEFAULT false,
    sort_order       integer     NOT NULL DEFAULT 0,
    catalog_source   text        NOT NULL DEFAULT 'CLOUD',
    synced_at        timestamptz NOT NULL DEFAULT now(),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_local_movies_source CHECK (catalog_source IN ('CLOUD', 'FLASHDISK'))
);

CREATE INDEX IF NOT EXISTS idx_movies_tenant ON mst_movies (tenant_id);
CREATE INDEX IF NOT EXISTS idx_movies_featured ON mst_movies (tenant_id, is_featured) WHERE is_featured;
CREATE INDEX IF NOT EXISTS idx_movies_genres ON mst_movies USING gin (genres);
CREATE INDEX IF NOT EXISTS idx_movies_title ON mst_movies (lower(title));

DROP TRIGGER IF EXISTS trg_movies_updated_at ON mst_movies;
CREATE TRIGGER trg_movies_updated_at
    BEFORE UPDATE ON mst_movies
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- mst_movie_details — cermin metadata
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mst_movie_details (
    movie_id          uuid PRIMARY KEY REFERENCES mst_movies (id) ON DELETE CASCADE,
    synopsis          text        NOT NULL DEFAULT '',
    tagline           text        NOT NULL DEFAULT '',
    director          text        NOT NULL DEFAULT '',
    cast_members      jsonb       NOT NULL DEFAULT '[]'::jsonb,
    country           text        NOT NULL DEFAULT '',
    original_language text        NOT NULL DEFAULT '',
    age_rating        text        NOT NULL DEFAULT '',
    quality           text        NOT NULL DEFAULT '',
    audio_tracks      jsonb       NOT NULL DEFAULT '[]'::jsonb,
    subtitles         jsonb       NOT NULL DEFAULT '[]'::jsonb,
    metadata_source   text        NOT NULL DEFAULT 'MANUAL',
    raw               jsonb       NOT NULL DEFAULT '{}'::jsonb,
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_details_source CHECK (metadata_source IN ('CATALOG_JSON', 'FILENAME', 'MANUAL'))
);

-- ---------------------------------------------------------------------------
-- mst_movie_local — BERKAS FISIK film pada server lokal ini
--
-- Inilah tabel yang menggantikan rancangan mst_movie_copies. Satu film hanya
-- punya satu berkas pada satu node, sehingga kuncinya UNIQUE (movie_id).
--
-- Alur status:
--   PENDING       dijadwalkan, berkas belum ada
--   TRANSFERRING  sedang disalin dari server utama
--   READY         berkas ada dan bisa diputar
--   MISSING       berkas pernah ada lalu hilang dari storage
--   FAILED        penyalinan gagal, lihat kolom error
--
-- source menandai asal berkas: CLOUD (unduhan dari server utama) atau
-- FLASHDISK (disalin manual ke folder Movies di server lokal).
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mst_movie_local (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid        NOT NULL REFERENCES mst_tenants (id) ON DELETE CASCADE,
    movie_id            uuid        NOT NULL REFERENCES mst_movies (id) ON DELETE CASCADE,
    node_code           text        NOT NULL DEFAULT '',
    status              text        NOT NULL DEFAULT 'PENDING',
    source              text        NOT NULL DEFAULT 'CLOUD',
    expected_filename   text        NOT NULL DEFAULT '',
    file_path           text        NOT NULL DEFAULT '',
    file_size_bytes     bigint      NOT NULL DEFAULT 0,
    checksum_sha256     text        NOT NULL DEFAULT '',
    container           text        NOT NULL DEFAULT '',
    progress_bytes      bigint      NOT NULL DEFAULT 0,
    error               text        NOT NULL DEFAULT '',
    assigned_at         timestamptz NOT NULL DEFAULT now(),
    transfer_started_at timestamptz,
    ready_at            timestamptz,
    scanned_at          timestamptz,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_movie_local_status CHECK (status IN ('PENDING', 'TRANSFERRING', 'READY', 'MISSING', 'FAILED')),
    CONSTRAINT chk_movie_local_source CHECK (source IN ('CLOUD', 'FLASHDISK')),
    CONSTRAINT uq_movie_local_movie UNIQUE (movie_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_movie_local_path
    ON mst_movie_local (tenant_id, file_path)
    WHERE file_path <> '';

CREATE INDEX IF NOT EXISTS idx_movie_local_status ON mst_movie_local (tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_movie_local_expected ON mst_movie_local (tenant_id, lower(expected_filename));

DROP TRIGGER IF EXISTS trg_movie_local_updated_at ON mst_movie_local;
CREATE TRIGGER trg_movie_local_updated_at
    BEFORE UPDATE ON mst_movie_local
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- mst_announcements — running text
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mst_announcements (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL REFERENCES mst_tenants (id) ON DELETE CASCADE,
    message     text        NOT NULL,
    severity    text        NOT NULL DEFAULT 'INFO',
    active_from timestamptz,
    active_to   timestamptz,
    is_active   boolean     NOT NULL DEFAULT true,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_announcements_severity CHECK (severity IN ('INFO', 'WARNING', 'EMERGENCY'))
);

CREATE INDEX IF NOT EXISTS idx_announcements_tenant_active ON mst_announcements (tenant_id, is_active);

DROP TRIGGER IF EXISTS trg_announcements_updated_at ON mst_announcements;
CREATE TRIGGER trg_announcements_updated_at
    BEFORE UPDATE ON mst_announcements
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- trn_remote_commands — instruksi untuk satu TV
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS trn_remote_commands (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid        NOT NULL REFERENCES mst_tenants (id) ON DELETE CASCADE,
    device_id  uuid        NOT NULL REFERENCES mst_tv_devices (id) ON DELETE CASCADE,
    command    text        NOT NULL,
    payload    jsonb       NOT NULL DEFAULT '{}'::jsonb,
    status     text        NOT NULL DEFAULT 'QUEUED',
    issued_by  text        NOT NULL DEFAULT '',
    issued_at  timestamptz NOT NULL DEFAULT now(),
    acked_at   timestamptz,
    error      text        NOT NULL DEFAULT '',
    CONSTRAINT chk_command_status CHECK (status IN ('QUEUED', 'SENT', 'ACKED', 'FAILED'))
);

CREATE INDEX IF NOT EXISTS idx_commands_device_status ON trn_remote_commands (device_id, status);
CREATE INDEX IF NOT EXISTS idx_commands_tenant ON trn_remote_commands (tenant_id, issued_at DESC);

-- ---------------------------------------------------------------------------
-- trn_watch_logs — progres tontonan per perangkat dan film
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS trn_watch_logs (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        uuid        NOT NULL REFERENCES mst_tenants (id) ON DELETE CASCADE,
    device_id        uuid        NOT NULL REFERENCES mst_tv_devices (id) ON DELETE CASCADE,
    movie_id         uuid        NOT NULL REFERENCES mst_movies (id) ON DELETE CASCADE,
    position_seconds integer     NOT NULL DEFAULT 0,
    duration_seconds integer     NOT NULL DEFAULT 0,
    completed        boolean     NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_watch_device_movie UNIQUE (device_id, movie_id)
);

CREATE INDEX IF NOT EXISTS idx_watch_tenant_updated ON trn_watch_logs (tenant_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_watch_movie ON trn_watch_logs (movie_id);

DROP TRIGGER IF EXISTS trg_watch_updated_at ON trn_watch_logs;
CREATE TRIGGER trg_watch_updated_at
    BEFORE UPDATE ON trn_watch_logs
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- trn_media_scans — riwayat pemindaian storage lokal
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS trn_media_scans (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid        NOT NULL REFERENCES mst_tenants (id) ON DELETE CASCADE,
    trigger       text        NOT NULL DEFAULT 'MANUAL',
    root_dir      text        NOT NULL,
    status        text        NOT NULL DEFAULT 'RUNNING',
    files_found   integer     NOT NULL DEFAULT 0,
    files_added   integer     NOT NULL DEFAULT 0,
    files_updated integer     NOT NULL DEFAULT 0,
    files_missing integer     NOT NULL DEFAULT 0,
    error         text        NOT NULL DEFAULT '',
    started_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz,
    CONSTRAINT chk_scan_status CHECK (status IN ('RUNNING', 'COMPLETED', 'FAILED'))
);

CREATE INDEX IF NOT EXISTS idx_scans_tenant_started ON trn_media_scans (tenant_id, started_at DESC);

-- ---------------------------------------------------------------------------
-- audit_logs — audit operasional lokal
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS audit_logs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid REFERENCES mst_tenants (id) ON DELETE SET NULL,
    actor_id   text        NOT NULL DEFAULT '',
    actor_role text        NOT NULL DEFAULT '',
    action     text        NOT NULL,
    entity     text        NOT NULL DEFAULT '',
    entity_id  text        NOT NULL DEFAULT '',
    ip         text        NOT NULL DEFAULT '',
    metadata   jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_tenant ON audit_logs (tenant_id, created_at DESC);
