-- ============================================================================
-- stayin_main — SERVER UTAMA (aplikasi CMS / authoring)
-- 001_main_schema.sql
--
-- Database ini adalah aplikasi yang berbeda dari server lokal di kapal.
-- Isinya hanya wewenang pusat: tenant, akun admin, node terdaftar, katalog
-- master (metadata film), rilis aplikasi, antrean distribusi dan audit.
--
-- TIDAK ADA kolom berkas fisik di sini. Keberadaan file nyata milik server
-- lokal dan disimpan pada tabel mst_movie_local di database stayin_local.
-- ============================================================================

CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

-- ---------------------------------------------------------------------------
-- mst_tenants — mitra whitelabel
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mst_tenants (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug            text        NOT NULL,
    name            text        NOT NULL,
    branding        jsonb       NOT NULL DEFAULT '{}'::jsonb,
    license_quota   integer     NOT NULL DEFAULT 50,
    status          text        NOT NULL DEFAULT 'ACTIVE',
    catalog_version integer     NOT NULL DEFAULT 0,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_tenants_status CHECK (status IN ('ACTIVE', 'SUSPENDED')),
    CONSTRAINT chk_tenants_quota CHECK (license_quota > 0)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_tenants_slug ON mst_tenants (lower(slug));

DROP TRIGGER IF EXISTS trg_tenants_updated_at ON mst_tenants;
CREATE TRIGGER trg_tenants_updated_at
    BEFORE UPDATE ON mst_tenants
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- mst_users — super admin (tenant_id NULL) dan admin mitra
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mst_users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     uuid REFERENCES mst_tenants (id) ON DELETE CASCADE,
    email         text        NOT NULL,
    password_hash text        NOT NULL,
    full_name     text        NOT NULL DEFAULT '',
    role          text        NOT NULL,
    status        text        NOT NULL DEFAULT 'ACTIVE',
    last_login_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_users_role CHECK (role IN ('SUPER_ADMIN', 'TENANT_ADMIN')),
    CONSTRAINT chk_users_status CHECK (status IN ('ACTIVE', 'DISABLED')),
    CONSTRAINT chk_users_tenant_binding CHECK (
        (role = 'SUPER_ADMIN' AND tenant_id IS NULL)
        OR (role = 'TENANT_ADMIN' AND tenant_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_users_email ON mst_users (lower(email));
CREATE INDEX IF NOT EXISTS idx_users_tenant ON mst_users (tenant_id);

DROP TRIGGER IF EXISTS trg_users_updated_at ON mst_users;
CREATE TRIGGER trg_users_updated_at
    BEFORE UPDATE ON mst_users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- mst_edge_nodes — server lokal kapal yang terdaftar
--
-- inventory menyimpan laporan terakhir dari node: peta movie_id -> status
-- mst_movie_local. Dengan begitu CMS tetap bisa menampilkan kondisi kapal
-- walau node sedang tidak terhubung.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mst_edge_nodes (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id         uuid        NOT NULL REFERENCES mst_tenants (id) ON DELETE CASCADE,
    node_code         text        NOT NULL,
    name              text        NOT NULL DEFAULT '',
    base_url          text        NOT NULL DEFAULT '',
    tunnel_ip         text        NOT NULL DEFAULT '',
    status            text        NOT NULL DEFAULT 'UNKNOWN',
    agent_version     text        NOT NULL DEFAULT '',
    metrics           jsonb       NOT NULL DEFAULT '{}'::jsonb,
    inventory         jsonb       NOT NULL DEFAULT '{}'::jsonb,
    last_heartbeat_at timestamptz,
    last_inventory_at timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_nodes_status CHECK (status IN ('ONLINE', 'OFFLINE', 'UNKNOWN')),
    CONSTRAINT uq_nodes_code UNIQUE (tenant_id, node_code)
);

CREATE INDEX IF NOT EXISTS idx_nodes_tenant ON mst_edge_nodes (tenant_id);

DROP TRIGGER IF EXISTS trg_nodes_updated_at ON mst_edge_nodes;
CREATE TRIGGER trg_nodes_updated_at
    BEFORE UPDATE ON mst_edge_nodes
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- mst_movies — katalog MASTER (identitas dan metadata saja)
--
-- status menandai tahap authoring, bukan kesiapan file:
--   DRAFT      metadata masih disusun
--   PENDING    sudah dipublikasikan, menunggu transfer ke server lokal
--   PUBLISHED  sudah didistribusikan ke minimal satu node
--   ARCHIVED   ditarik dari peredaran
--
-- Kolom file_path, file_size_bytes, checksum_sha256 dan container SENGAJA
-- tidak ada di sini; semuanya milik mst_movie_local di stayin_local.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mst_movies (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
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
    status           text        NOT NULL DEFAULT 'DRAFT',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_movies_status CHECK (status IN ('DRAFT', 'PENDING', 'PUBLISHED', 'ARCHIVED')),
    CONSTRAINT uq_movies_slug UNIQUE (tenant_id, slug)
);

CREATE INDEX IF NOT EXISTS idx_movies_tenant_status ON mst_movies (tenant_id, status);
CREATE INDEX IF NOT EXISTS idx_movies_tenant_featured ON mst_movies (tenant_id, is_featured) WHERE is_featured;
CREATE INDEX IF NOT EXISTS idx_movies_genres ON mst_movies USING gin (genres);
CREATE INDEX IF NOT EXISTS idx_movies_title ON mst_movies (lower(title));

DROP TRIGGER IF EXISTS trg_movies_updated_at ON mst_movies;
CREATE TRIGGER trg_movies_updated_at
    BEFORE UPDATE ON mst_movies
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- mst_movie_details — metadata kaya, satu baris per film
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
-- mst_app_releases — paket OTA untuk TV Android
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mst_app_releases (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       uuid REFERENCES mst_tenants (id) ON DELETE CASCADE,
    version_code    integer     NOT NULL,
    version_name    text        NOT NULL,
    file_path       text        NOT NULL,
    file_size_bytes bigint      NOT NULL DEFAULT 0,
    checksum_sha256 text        NOT NULL DEFAULT '',
    changelog       text        NOT NULL DEFAULT '',
    is_active       boolean     NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_releases_tenant ON mst_app_releases (tenant_id, version_code DESC);

DROP TRIGGER IF EXISTS trg_releases_updated_at ON mst_app_releases;
CREATE TRIGGER trg_releases_updated_at
    BEFORE UPDATE ON mst_app_releases
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- trn_sync_jobs — antrean distribusi utama -> node
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS trn_sync_jobs (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   uuid        NOT NULL REFERENCES mst_tenants (id) ON DELETE CASCADE,
    node_id     uuid REFERENCES mst_edge_nodes (id) ON DELETE SET NULL,
    type        text        NOT NULL,
    payload     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    status      text        NOT NULL DEFAULT 'QUEUED',
    bytes_total bigint      NOT NULL DEFAULT 0,
    bytes_done  bigint      NOT NULL DEFAULT 0,
    error       text        NOT NULL DEFAULT '',
    started_at  timestamptz,
    finished_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_sync_status CHECK (status IN ('QUEUED', 'RUNNING', 'COMPLETED', 'FAILED'))
);

CREATE INDEX IF NOT EXISTS idx_sync_status_created ON trn_sync_jobs (status, created_at);
CREATE INDEX IF NOT EXISTS idx_sync_tenant ON trn_sync_jobs (tenant_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- audit_logs — jejak tindakan administratif pada server utama
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS audit_logs (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid REFERENCES mst_tenants (id) ON DELETE SET NULL,
    actor_id   uuid REFERENCES mst_users (id) ON DELETE SET NULL,
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
CREATE INDEX IF NOT EXISTS idx_audit_actor ON audit_logs (actor_id, created_at DESC);

-- ---------------------------------------------------------------------------
-- mst_app_configs — parameter aplikasi global dan per tenant
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mst_app_configs (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           uuid UNIQUE REFERENCES mst_tenants (id) ON DELETE CASCADE,
    heartbeat_seconds   integer NOT NULL DEFAULT 30,
    sync_seconds        integer NOT NULL DEFAULT 300,
    minimum_app_version text    NOT NULL DEFAULT '1.0.0',
    stream_timeout_secs integer NOT NULL DEFAULT 3600,
    paused_screen_text  text    NOT NULL DEFAULT 'Siaran dijeda oleh kru kapal.',
    updated_at          timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT chk_app_configs_heartbeat CHECK (heartbeat_seconds BETWEEN 5 AND 600),
    CONSTRAINT chk_app_configs_sync CHECK (sync_seconds BETWEEN 10 AND 86400)
);

DROP TRIGGER IF EXISTS trg_app_configs_updated_at ON mst_app_configs;
CREATE TRIGGER trg_app_configs_updated_at
    BEFORE UPDATE ON mst_app_configs
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- ---------------------------------------------------------------------------
-- mst_feature_flags — saklar fitur per tenant
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS mst_feature_flags (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id  uuid        NOT NULL REFERENCES mst_tenants (id) ON DELETE CASCADE,
    flag_key   text        NOT NULL,
    enabled    boolean     NOT NULL DEFAULT false,
    note       text        NOT NULL DEFAULT '',
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_feature_flags UNIQUE (tenant_id, flag_key)
);

DROP TRIGGER IF EXISTS trg_feature_flags_updated_at ON mst_feature_flags;
CREATE TRIGGER trg_feature_flags_updated_at
    BEFORE UPDATE ON mst_feature_flags
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();
