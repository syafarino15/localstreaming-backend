# StayIn — Backend (Go)

Backend untuk sistem hiburan onboard **DLU X ALFATEVE**. Produk ini berjalan
sebagai **dua aplikasi dengan dua database PostgreSQL terpisah**:

| Aplikasi | Database | Isi |
|---|---|---|
| Server utama (CMS) | `stayin_main` | tenant, akun admin, katalog master, detail film, rilis APK, antrean distribusi, audit |
| Server lokal (kapal) | `stayin_local` | `mst_movie_local` (berkas nyata), kamar, TV, layout halaman awal, pengumuman, progres tonton, perintah remote |

Katalog master **tidak menyimpan path file**. Satu-satunya tempat path, ukuran,
checksum, dan status berkas adalah `mst_movie_local` di server lokal.

## Ringkasan teknis

| Aspek | Implementasi |
|---|---|
| Bahasa | Go 1.24+ |
| Router | Gin v1 |
| Database | PostgreSQL 16, `pgx/v5` + `pgxpool`, dua pool terpisah |
| Migrasi | SQL tertanam (`embed.FS`), satu set per database |
| Real-time | `gorilla/websocket` |
| Auth | JWT HS256 (admin) + device token (TV) + node key (inventory) |
| Log | `zerolog` JSON terstruktur |
| Streaming | HTTP byte-range native (`206`), buffer pool `sync.Pool` |
| Arsitektur | `handler → usecase → repository` (clean architecture) |

## Struktur

```
cmd/api/main.go              # composition root, dua pool, graceful shutdown
internal/
  domain/                    # entity, DTO, kontrak repository, notifier
  usecase/                   # auth, tv (lokal), admin (utama), media
  repository/postgres/       # Store = stayin_main, Local = stayin_local
  delivery/http/             # router, middleware, handler
  delivery/ws/               # hub + handler WebSocket
  worker/media/              # ingest flashdisk -> mst_movie_local
  worker/sync/               # worker antrean trn_sync_jobs
  platform/                  # config, logger, auth, migrator, storage, httpx
pkg/response/                # envelope JSON
db/migrations/main/          # skema stayin_main + seed
db/migrations/local/         # skema stayin_local + seed
docs/openapi.yaml            # kontrak API (sumber tunggal codegen)
```

## Menjalankan

### Prasyarat

- Go 1.24 atau lebih baru
- PostgreSQL 16 (bisa container atau instalasi lokal)

### Langkah

```bash
# Buat dua database terpisah pada instance PostgreSQL yang sama (dev)
createdb -h 127.0.0.1 -p 5433 -U stayin stayin_main
createdb -h 127.0.0.1 -p 5433 -U stayin stayin_local

cp .env.example .env      # sesuaikan MAIN_DATABASE_URL, LOCAL_DATABASE_URL, JWT_SECRET
go run ./cmd/api
```

Server akan otomatis: menerapkan migrasi `stayin_main` dan `stayin_local`,
menjalankan seed tenant `dlu-alfateve`, membuat akun bootstrap, dan memindai
folder media.

Cek kesehatan:

```bash
curl http://127.0.0.1:8080/healthz
```

Referensi API offline: `http://127.0.0.1:8080/swagger/index.html`

### Akun bootstrap

Dibuat hanya ketika tabel pengguna masih kosong:

| Peran | Email | Password |
|---|---|---|
| Super Admin | `owner@stayin.local` | `Owner#2026` |
| Admin Mitra | `admin@dlu-alfateve.local` | `Mitra#2026` |

Ubah keduanya di `.env` sebelum dipakai produksi.

## Mode aplikasi

`APP_MODE` menentukan aplikasi mana yang dijalankan proses ini:

| Mode | Database yang dibuka | Route aktif |
|---|---|---|
| `combined` | `stayin_main` + `stayin_local` | TV + streaming + WS + admin CMS (dev / mini PC) |
| `main` | `stayin_main` saja | auth admin, CMS, katalog master, distribusi |
| `local` | `stayin_local` saja | TV, streaming, WS, ingest, layout |

`combined` bukan penggabungan database: dua pool tetap terpisah dan setiap
receiver repository hanya bisa menyentuh database miliknya. Endpoint operasional
(rooms, devices, layout, announcements) mengembalikan `503` pada mode `main`
karena datanya memang ada di kapal.

## Alur konten: CMS → server lokal, atau flashdisk

Dua jalur masuk yang berujung pada tabel yang sama:

```
CMS (server utama)                      Flashdisk (server lokal)
mst_movies (PENDING)                    file masuk MEDIA_ROOT/Movies
       │                                        │
       ├── POST /tenant/movies                  ├── mesin ingest
       │   membuat placeholder                  │   cocokkan expected_filename
       ▼                                        ▼
mst_movie_local (PENDING) ── transfer ──► mst_movie_local (READY)
                                                │
                                                ├── /api/v1/stream/{movie_id}
                                                └── inventory report → CMS
```

1. Admin mitra menambah film di CMS. Tidak ada unggahan video. Baris master
   dibuat `PENDING` dan `mst_movie_local` dibuat `PENDING` dengan
   `expected_filename`.
2. Berkas tiba di kapal: lewat transfer jaringan, atau kru menyalinnya ke
   `MEDIA_ROOT/Movies` dari flashdisk.
3. Mesin ingest menemukan file, mencocokkannya dengan `expected_filename`
   (bukan membuat duplikat), lalu mengisi `file_path`, ukuran, container, dan
   checksum di `mst_movie_local`. Status menjadi `READY`.
4. Node mengirim laporan inventory; film `READY` dipromosikan menjadi
   `PUBLISHED` di katalog master sehingga CMS menampilkannya sebagai tersedia.
5. File yang hilang ditandai `MISSING`; baris tidak dihapus agar metadata
   manual tetap tersimpan bila file kembali.

Konten yang hanya datang lewat flashdisk tetap muncul di CMS: ingest membuat
baris katalog master dengan `id` yang sama, lalu inventory report menandainya
`PUBLISHED`.

### Metadata

| Sumber | Prioritas |
|---|---|
| `catalog.json` di folder film | tertinggi |
| `<nama-film>.json` sidecar | kedua |
| Parsing nama file | fallback |

`catalog.json` menerima objek `{ "movies": [...] }` maupun array langsung:

```json
{
  "movies": [
    {
      "file": "Inception 2010 720p.mp4",
      "title": "Inception",
      "year": 2010,
      "genres": ["Action", "Sci-Fi"],
      "synopsis": "Pencuri mimpi profesional...",
      "director": "Christopher Nolan",
      "cast": ["Leonardo DiCaprio"],
      "quality": "720p",
      "duration_seconds": 8880
    }
  ]
}
```

Tanpa `catalog.json`, `Laskar_Pelangi.2008.1080p.WEB-DL.mkv` menjadi judul
`Laskar Pelangi` tahun `2008`. Poster `.jpg`/`.png` dan subtitle `.srt`/`.vtt`
dengan nama serupa otomatis dipasangkan.

## Streaming

`GET /api/v1/stream/{movieID}` melayani berkas apa adanya:

- `Accept-Ranges: bytes` pada setiap respons
- `206 Partial Content` + `Content-Range` untuk permintaan range
- `416` untuk range di luar ukuran berkas
- `304` saat `If-None-Match` cocok
- `HEAD` untuk membaca header saja

Tidak ada transcoding. File `.mp4`/`.mkv` diputar progresif, `.m3u8` disajikan
passthrough. Buffer dibaca memakai `sync.Pool` dan jumlah berkas terbuka dibatasi
`STREAM_MAX_OPEN_FILES`.

Hasil uji pada mesin pengembangan: 50 klien konkuren × 25.000 permintaan range,
0 gagal, RSS server 42,7 MB.

## WebSocket

| Endpoint | Pemakai | Kredensial |
|---|---|---|
| `/ws/tv` | aplikasi TV | device token |
| `/ws/remote` | PWA penumpang | `room_id` + `tenant_id` |

Event server → TV: `WELCOME`, `COMMAND`, `REFRESH_CONFIG`,
`ROOM_GUEST_UPDATED`, `ANNOUNCEMENT`, `CONTENT_UPDATED`, `APK_AVAILABLE`.

Perintah yang dikirim saat TV offline tetap `QUEUED` dan dikirim ulang saat TV
tersambung kembali.

## Keamanan

- Isolasi tenant: setiap query CMS memakai `tenant_id` dari JWT, bukan dari input.
- Token perangkat disimpan sebagai SHA-256; plaintext hanya keluar sekali saat pairing.
- Path media selalu dinormalisasi; percobaan keluar dari media root ditolak.
- Seluruh query berparameter (tidak ada konkatenasi SQL).
- Body request JSON divalidasi di layer usecase dengan error per field.

## Perintah

```bash
go build ./...          # kompilasi
go vet ./...            # analisis statis
go test ./...           # unit test
gofmt -l .              # cek format
go run ./cmd/api        # jalankan
```

## Codegen kontrak untuk CMS dan TV

`docs/openapi.yaml` adalah sumber tunggal. Di repo Frontend maupun TVApp:

```bash
npm run gen:api           # membaca ../Backend/docs/openapi.yaml
OPENAPI_URL=http://192.168.1.10:8080/swagger/openapi.yaml npm run gen:api
```
