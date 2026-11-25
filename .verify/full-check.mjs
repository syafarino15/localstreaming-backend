/**
 * Comprehensive backend verification.
 *
 * Covers every endpoint group, both admin roles, tenant isolation, the media
 * ingest engine, byte-range streaming, the two-database split and the error
 * envelope shape.
 *
 * Run: node .verify/full-check.mjs
 *
 * The device block consumes seeded pairing codes, so re-run it against a fresh
 * node or restore them first:
 *   psql -h 127.0.0.1 -p 5433 -U stayin -d stayin_local -f .verify/reset-device-state.sql
 */
import fs from "node:fs";
import path from "node:path";

const BASE = "http://127.0.0.1:8080";
const results = [];

function record(group, name, ok, detail = "") {
  results.push({ group, name, ok, detail });
  const mark = ok ? "PASS" : "FAIL";
  console.log(`  ${mark}  ${name}${detail ? "  [" + detail + "]" : ""}`);
}

function group(name) {
  console.log(`\n== ${name} ==`);
}

async function call(pathname, { method = "GET", token, body, deviceToken, headers = {} } = {}) {
  const h = { Accept: "application/json", ...headers };
  if (body !== undefined) h["Content-Type"] = "application/json";
  if (token) h.Authorization = `Bearer ${token}`;
  if (deviceToken) h["X-Device-Token"] = deviceToken;

  const res = await fetch(BASE + pathname, {
    method,
    headers: h,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let json = null;
  try {
    json = text ? JSON.parse(text) : null;
  } catch {
    json = null;
  }
  return { status: res.status, json, text, headers: res.headers };
}

// ---------------------------------------------------------------------------
group("Health & docs");
{
  const health = await call("/healthz");
  record("health", "GET /healthz", health.status === 200 && health.json?.data?.database === "ok");

  const spec = await call("/swagger/openapi.yaml");
  record("docs", "OpenAPI spec served", spec.status === 200 && spec.text.includes("openapi: 3.0.3"));

  const ui = await call("/swagger/index.html");
  record("docs", "Swagger UI page served", ui.status === 200 && ui.text.includes("StayIn"));
}

// ---------------------------------------------------------------------------
group("Authentication");
const owner = await call("/api/v1/auth/login", {
  method: "POST",
  body: { email: "owner@stayin.local", password: "Owner#2026" },
});
const ownerToken = owner.json?.data?.access_token ?? "";
record("auth", "Super admin login", owner.status === 200 && ownerToken.length > 50);
record("auth", "Login sets httpOnly cookie", (owner.headers.get("set-cookie") ?? "").includes("access_token"));

const tenant = await call("/api/v1/auth/login", {
  method: "POST",
  body: { email: "admin@dlu-alfateve.local", password: "Mitra#2026" },
});
const tenantToken = tenant.json?.data?.access_token ?? "";
record("auth", "Tenant admin login", tenant.status === 200 && tenantToken.length > 50);

// Persist the admin token so the websocket check can reuse a live session.
fs.writeFileSync(".verify/token.txt", tenantToken);

const badLogin = await call("/api/v1/auth/login", {
  method: "POST",
  body: { email: "owner@stayin.local", password: "wrong-password" },
});
record(
  "auth",
  "Wrong password rejected with envelope",
  badLogin.status === 401 && badLogin.json?.success === false && typeof badLogin.json?.message === "string",
);

const noAuth = await call("/api/v1/tenant/overview");
record("auth", "Protected route without token returns 401", noAuth.status === 401);

const me = await call("/api/v1/admin/me", { token: ownerToken });
record("auth", "GET /admin/me", me.status === 200 && me.json?.data?.role === "SUPER_ADMIN");

// ---------------------------------------------------------------------------
group("Super admin");
{
  const overview = await call("/api/v1/admin/overview", { token: ownerToken });
  const d = overview.json?.data;
  record(
    "super",
    "Overview aggregates fleet",
    overview.status === 200 && typeof d?.total_tenants === "number" && typeof d?.online_devices === "number",
    `tenants=${d?.total_tenants} devices=${d?.total_devices} movies=${d?.total_movies}`,
  );

  const tenants = await call("/api/v1/admin/tenants", { token: ownerToken });
  record("super", "List tenants", tenants.status === 200 && (tenants.json?.data ?? []).length >= 1);

  const users = await call("/api/v1/admin/users", { token: ownerToken });
  record("super", "List users", users.status === 200 && (users.json?.data ?? []).length >= 2);

  const nodes = await call("/api/v1/admin/nodes", { token: ownerToken });
  record("super", "List nodes", nodes.status === 200 && (nodes.json?.data ?? []).length >= 1);

  const audit = await call("/api/v1/admin/audit-logs", { token: ownerToken });
  record("super", "Audit trail records logins", audit.status === 200 && (audit.json?.data ?? []).length >= 1);

  const devices = await call("/api/v1/admin/devices", { token: ownerToken });
  record("super", "Fleet device list", devices.status === 200 && (devices.json?.meta?.total_data ?? 0) >= 12);

  const forbidden = await call("/api/v1/admin/tenants", { token: tenantToken });
  record("super", "Tenant admin blocked from tenant CRUD", forbidden.status === 403);
}

// ---------------------------------------------------------------------------
group("Tenant admin");
{
  const overview = await call("/api/v1/tenant/overview", { token: tenantToken });
  const d = overview.json?.data;
  record(
    "tenant",
    "Overview scoped to tenant",
    overview.status === 200 && d?.total_rooms >= 12,
    `rooms=${d?.total_rooms} devices=${d?.total_devices} movies=${d?.ready_movies}`,
  );

  const rooms = await call("/api/v1/tenant/rooms?limit=50", { token: tenantToken });
  record("tenant", "List rooms", rooms.status === 200 && (rooms.json?.data ?? []).length >= 12);

  const movies = await call("/api/v1/tenant/movies", { token: tenantToken });
  record(
    "tenant",
    "List movies",
    movies.status === 200 && (movies.json?.meta?.total_data ?? 0) >= 2,
    `total=${movies.json?.meta?.total_data}`,
  );

  const layout = await call("/api/v1/tenant/layout", { token: tenantToken });
  record(
    "tenant",
    "Read layout document",
    layout.status === 200 && layout.json?.data?.welcome_screen?.layout_variant === "SPLIT_LEFT_CARD",
  );

  const announcements = await call("/api/v1/tenant/announcements", { token: tenantToken });
  record("tenant", "List announcements", announcements.status === 200 && (announcements.json?.data ?? []).length >= 2);

  const devices = await call("/api/v1/tenant/devices", { token: tenantToken });
  record("tenant", "List tenant devices", devices.status === 200 && (devices.json?.meta?.total_data ?? 0) >= 12);

  const scans = await call("/api/v1/tenant/scans", { token: tenantToken });
  record("tenant", "Scan history present", scans.status === 200 && (scans.json?.data ?? []).length >= 1);

  const syncJobs = await call("/api/v1/tenant/sync-jobs", { token: tenantToken });
  record("tenant", "Sync queue readable", syncJobs.status === 200);
}

// ---------------------------------------------------------------------------
group("Tenant isolation (IDOR)");
{
  // Create a second tenant and confirm the tenant admin cannot reach it.
  const suffix = Date.now().toString(36);
  const created = await call("/api/v1/admin/tenants", {
    method: "POST",
    token: ownerToken,
    body: { slug: `isolation-${suffix}`, name: "Isolation Test", license_quota: 5 },
  });
  const otherTenantId = created.json?.data?.id ?? "";
  record("isolation", "Create second tenant", created.status === 201 && otherTenantId !== "");

  const otherUser = await call("/api/v1/admin/users", {
    method: "POST",
    token: ownerToken,
    body: {
      email: `iso-${suffix}@test.local`,
      password: "IsoTest#2026",
      full_name: "Isolation Admin",
      role: "TENANT_ADMIN",
      tenant_id: otherTenantId,
    },
  });
  record("isolation", "Create second tenant admin", otherUser.status === 201);

  const otherLogin = await call("/api/v1/auth/login", {
    method: "POST",
    body: { email: `iso-${suffix}@test.local`, password: "IsoTest#2026" },
  });
  const otherToken = otherLogin.json?.data?.access_token ?? "";
  record("isolation", "Second tenant admin can log in", otherLogin.status === 200);

  const otherOverview = await call("/api/v1/tenant/overview", { token: otherToken });
  const d = otherOverview.json?.data;
  record(
    "isolation",
    "Second tenant sees only its own empty scope",
    otherOverview.status === 200 && d?.total_rooms === 0 && d?.total_movies === 0,
  );

  const otherMovies = await call("/api/v1/tenant/movies", { token: otherToken });
  record(
    "isolation",
    "Second tenant cannot read DLU movies",
    otherMovies.status === 200 && (otherMovies.json?.meta?.total_data ?? -1) === 0,
  );

  // A tenant admin must not read another tenant's movie by guessing its id.
  const dluMovies = await call("/api/v1/tenant/movies", { token: tenantToken });
  const dluMovieId = dluMovies.json?.data?.[0]?.movie?.id ?? "";
  const crossRead = await call(`/api/v1/tenant/movies/${dluMovieId}`, { token: otherToken });
  record("isolation", "Cross-tenant movie read is 404", crossRead.status === 404);

  const crossDelete = await call(`/api/v1/tenant/movies/${dluMovieId}`, {
    method: "DELETE",
    token: otherToken,
  });
  record("isolation", "Cross-tenant movie delete is 404", crossDelete.status === 404);

  // Clean up.
  await call(`/api/v1/admin/tenants/${otherTenantId}/status`, {
    method: "POST",
    token: ownerToken,
    body: { status: "SUSPENDED" },
  });
}

// ---------------------------------------------------------------------------
group("Device lifecycle");
let deviceToken = "";
let deviceUid = "";
let deviceRoomId = "";
let guestRoomId = "";
{
  const devices = await call("/api/v1/tenant/devices?status=UNPAIRED&limit=50", { token: tenantToken });
  const candidate = (devices.json?.data ?? []).find((d) => d.pairing_code);
  record("device", "An unpaired device with a pairing code exists", Boolean(candidate), candidate?.device_uid ?? "");

  if (candidate) {
    const pairingCode = candidate.pairing_code;
    deviceUid = `VERIFY-TV-${Date.now().toString(36)}`;
    const pair = await call("/api/v1/auth/device/pair", {
      method: "POST",
      body: { device_uid: deviceUid, pairing_code: pairingCode, model: "Verification", app_version: "1.0.0" },
    });
    deviceToken = pair.json?.data?.device_token ?? "";
    deviceRoomId = pair.json?.data?.room_id ?? "";
    record("device", "Pair device with code", pair.status === 200 && deviceToken.startsWith("dvt_"));

    // Persist the device credential so the websocket check can reconnect with
    // a token that exists in the current local database.
    if (deviceToken) {
      fs.writeFileSync(".verify/devtoken.txt", deviceToken);
      fs.writeFileSync(
        ".verify/dev.json",
        JSON.stringify({ device_uid: deviceUid, room_id: deviceRoomId, paired_at: new Date().toISOString() }, null, 2),
      );
    }

    const reuse = await call("/api/v1/auth/device/pair", {
      method: "POST",
      body: { device_uid: deviceUid, pairing_code: pairingCode, model: "Verification", app_version: "1.0.0" },
    });
    record("device", "Pairing code cannot be reused", reuse.status === 401);

    const init = await call(`/api/v1/tv/init?device_id=${encodeURIComponent(deviceUid)}`, { deviceToken });
    const d = init.json?.data;
    record("device", "TV init returns layout", init.status === 200 && d?.layout_config?.app_title === "DLU X ALFATEVE");
    record("device", "TV init returns catalog rows", (d?.catalog?.rows ?? []).length >= 1, `rows=${d?.catalog?.rows?.length}`);
    record("device", "TV init returns announcements", (d?.announcements ?? []).length >= 2);
    record("device", "TV init returns server metadata", d?.server?.streaming_mode === "byte-range");

    const mismatched = await call("/api/v1/tv/init?device_id=some-other-tv", { deviceToken });
    record("device", "device_id mismatch rejected", mismatched.status === 403);

    const badToken = await call("/api/v1/tv/init", { deviceToken: "dvt_not_a_real_token" });
    record("device", "Invalid device token rejected", badToken.status === 401);

    const member = (d?.catalog?.rows?.[0]?.movies ?? [])[0];
    if (member) {
      const detail = await call(`/api/v1/tv/movies/${member.id}`, { deviceToken });
      record("device", "Movie detail",
        detail.status === 200 && (detail.json?.data?.title ?? "").length > 0,
        detail.json?.data?.title ?? "");

      const hb = await call("/api/v1/tv/heartbeat", {
        method: "POST",
        deviceToken,
        body: { app_version: "1.0.0-verify", state: "CATALOG" },
      });
      record("device", "Heartbeat accepted", hb.status === 200);

      const watch = await call("/api/v1/tv/watch-logs", {
        method: "POST",
        deviceToken,
        body: { movie_id: member.id, position_seconds: 42, duration_seconds: 100, completed: false },
      });
      record("device", "Watch log stored", watch.status === 200);

      const resume = await call("/api/v1/tv/resume", { deviceToken });
      record("device", "Resume list returns the entry", (resume.json?.data?.items ?? []).length >= 1);

      const online = await call("/api/v1/tenant/devices?status=ONLINE&limit=50", { token: tenantToken });
      record(
        "device",
        "Device shows ONLINE after init",
        (online.json?.data ?? []).some((d) => d.device_uid === deviceUid),
      );
    }

    // Remote command round trip.
    const deviceId = pair.json?.data?.device_id;
    if (deviceId) {
      const cmd = await call(`/api/v1/tenant/devices/${deviceId}/commands`, {
        method: "POST",
        token: tenantToken,
        body: { command: "REFRESH_CONFIG", payload: { reason: "verify" } },
      });
      record("device", "Remote command queued", cmd.status === 201 && cmd.json?.data?.status === "QUEUED");

      const pending = await call("/api/v1/tv/commands", { deviceToken });
      record("device", "Device drains queued command", (pending.json?.data?.items ?? []).length >= 1);

      const invalid = await call(`/api/v1/tenant/devices/${deviceId}/commands`, {
        method: "POST",
        token: tenantToken,
        body: { command: "FORMAT_DISK" },
      });
      record("device", "Unknown command rejected", invalid.status === 400);
    }
  }
}

// ---------------------------------------------------------------------------
group("Streaming (byte range)");
{
  const movies = await call("/api/v1/tenant/movies?search=Laskar", { token: tenantToken });
  const item = (movies.json?.data ?? [])[0];
  const movie = item?.movie;
  const local = item?.local;
  record("stream", "Test movie available", Boolean(movie && local), movie?.title ?? "");

  if (movie && local) {
    const fileSize = local.file_size_bytes;
    const url = `${BASE}/api/v1/stream/${movie.id}`;

    // The load test streams this same file; the id changes with every fresh
    // database, so it is persisted rather than hardcoded.
    fs.writeFileSync(".verify/laskar_id.txt", movie.id);

    const full = await fetch(url);
    const fullBuf = Buffer.from(await full.arrayBuffer());
    record(
      "stream",
      "Full GET returns the whole file",
      full.status === 200 && fullBuf.length === fileSize,
      `${fullBuf.length} bytes`,
    );

    const head = await fetch(url, { method: "HEAD" });
    record(
      "stream",
      "HEAD advertises byte ranges",
      head.status === 200 && head.headers.get("accept-ranges") === "bytes" && head.headers.get("content-length") === String(fileSize),
    );

    const part = await fetch(url, { headers: { Range: "bytes=0-99" } });
    const partBuf = Buffer.from(await part.arrayBuffer());
    record(
      "stream",
      "Range 0-99 yields 206 with correct Content-Range",
      part.status === 206 &&
        part.headers.get("content-range") === `bytes 0-99/${fileSize}` &&
        partBuf.length === 100,
      part.headers.get("content-range") ?? "",
    );
    record("stream", "Range body starts at byte 0", partBuf[0] === 0 && partBuf[1] === 1);

    const tail = await fetch(url, { headers: { Range: "bytes=-50" } });
    record(
      "stream",
      "Suffix range serves the tail",
      tail.status === 206 && tail.headers.get("content-range") === `bytes ${fileSize - 50}-${fileSize - 1}/${fileSize}`,
    );

    const open = await fetch(url, { headers: { Range: `bytes=${fileSize - 10}-` } });
    record("stream", "Open-ended range", open.status === 206);

    const bad = await fetch(url, { headers: { Range: `bytes=${fileSize + 500}-` } });
    record(
      "stream",
      "Unsatisfiable range returns 416",
      bad.status === 416 && bad.headers.get("content-range") === `bytes */${fileSize}`,
    );

    const etag = head.headers.get("etag") ?? "";
    if (etag) {
      const conditional = await fetch(url, { headers: { "If-None-Match": etag } });
      record("stream", "If-None-Match returns 304", conditional.status === 304);
    }

    const missing = await fetch(`${BASE}/api/v1/stream/00000000-0000-4000-8000-000000000000`);
    record("stream", "Unknown movie returns 404", missing.status === 404);
  }
}

// ---------------------------------------------------------------------------
group("Assets & path safety");
{
  const poster = await fetch(`${BASE}/api/v1/assets/Movies/Laskar%20Pelangi%20(2008).jpg`);
  record("assets", "Poster served with image type", poster.status === 200 && (poster.headers.get("content-type") ?? "").includes("image/"));

  const subtitle = await fetch(`${BASE}/api/v1/assets/Movies/Laskar%20Pelangi%20(2008).id.srt`);
  const subText = await subtitle.text();
  record("assets", "Subtitle served", subtitle.status === 200 && subText.includes("-->"), `${subText.length} bytes`);

  const traversal = await fetch(`${BASE}/api/v1/assets/..%2F..%2Fgo.mod`);
  record("assets", "Encoded traversal blocked", traversal.status === 403 || traversal.status === 404, `status=${traversal.status}`);

  const traversal2 = await fetch(`${BASE}/api/v1/assets/../../Backend/go.mod`);
  record("assets", "Plain traversal blocked", traversal2.status === 403 || traversal2.status === 404, `status=${traversal2.status}`);
}

// ---------------------------------------------------------------------------
group("Media ingest engine");
{
  const moviesDir = "D:/Kerja/Alfa Teve/IPTV Media/Storage/Movies";
  // A unique name keeps the check idempotent: a previous run may have left a
  // MISSING row behind, and reusing the name would update instead of insert.
  const unique = String(Date.now());
  const probeTitle = `Probe Movie ${unique}`;
  const probeDir = path.join(moviesDir, `Verify-${unique}`);
  const probeFile = path.join(probeDir, `${probeTitle} 2024 720p.mp4`);
  fs.mkdirSync(probeDir, { recursive: true });
  fs.writeFileSync(probeFile, Buffer.alloc(4096, 7));

  const before = await call("/api/v1/tenant/movies", { token: tenantToken });
  const beforeCount = before.json?.meta?.total_data ?? 0;

  const scan = await call("/api/v1/tenant/scans", { method: "POST", token: tenantToken });
  record("ingest", "Manual scan completes", scan.status === 200 && scan.json?.data?.status === "COMPLETED");

  const after = await call(`/api/v1/tenant/movies?search=${encodeURIComponent(probeTitle)}`, { token: tenantToken });
  const added = (after.json?.data ?? [])[0];
  const addedMovie = added?.movie;
  const addedLocal = added?.local;
  record("ingest", "New file becomes a catalog row", Boolean(addedMovie && addedLocal), addedMovie?.title ?? "");
  if (addedMovie && addedLocal) {
    record("ingest", "Filename parsed into title + year", addedMovie.title === probeTitle && addedMovie.year === 2024);
    record("ingest", "File path recorded in mst_movie_local", String(addedLocal.file_path).startsWith("Movies/"));
    record("ingest", "Local row marked as flashdisk source", addedLocal.source === "FLASHDISK");
    record("ingest", "Local row status is READY", addedLocal.status === "READY");
  }

  const totalAfter = (await call("/api/v1/tenant/movies", { token: tenantToken })).json?.meta?.total_data ?? 0;
  record("ingest", "Catalog grew by exactly one", totalAfter === beforeCount + 1, `${beforeCount} -> ${totalAfter}`);

  // Removing the file must mark the row MISSING, not delete it.
  fs.unlinkSync(probeFile);
  await call("/api/v1/tenant/scans", { method: "POST", token: tenantToken });
  const missing = await call(`/api/v1/tenant/movies?search=${encodeURIComponent(probeTitle)}`, { token: tenantToken });
  const row = (missing.json?.data ?? [])[0];
  record("ingest", "Removed file becomes MISSING", row?.local?.status === "MISSING", row?.local?.status ?? "");

  const scanHistory = await call("/api/v1/tenant/scans", { token: tenantToken });
  const runs = scanHistory.json?.data ?? [];
  record(
    "ingest",
    "Scan runs record file counters",
    runs.length >= 2 && typeof runs[0].files_found === "number",
    `latest found=${runs[0]?.files_found} missing=${runs[0]?.files_missing}`,
  );

  fs.rmSync(probeDir, { recursive: true, force: true });
}

// ---------------------------------------------------------------------------
group("Database separation (main vs local)");
{
  // A CMS upload creates a master catalog row in stayin_main plus a PENDING
  // placeholder in stayin_local. No file exists yet, so the CMS must show the
  // movie as pending rather than ready.
  const unique = Date.now().toString(36);
  const title = `Separation Probe ${unique}`;
  const created = await call("/api/v1/tenant/movies", {
    method: "POST",
    token: tenantToken,
    body: {
      title,
      year: 2025,
      genres: ["Dokumenter"],
      expected_filename: `${title} 2025 1080p.mkv`,
      publish: true,
    },
  });
  const createdMovie = created.json?.data?.movie;
  const createdLocal = created.json?.data?.local;
  record("separation", "CMS creates a master catalog entry", created.status === 201 && Boolean(createdMovie));
  record(
    "separation",
    "New entry starts as PENDING, not READY",
    createdMovie?.status === "PENDING",
    createdMovie?.status ?? "",
  );
  record(
    "separation",
    "Ship placeholder created in mst_movie_local",
    createdLocal?.status === "PENDING" && (createdLocal?.expected_filename ?? "").length > 0,
    createdLocal?.status ?? "",
  );
  record(
    "separation",
    "Master row carries no filesystem path",
    createdMovie !== undefined && !("file_path" in createdMovie) && !("file_size_bytes" in createdMovie),
  );

  if (createdMovie) {
    const detail = await call(`/api/v1/tenant/movies/${createdMovie.id}`, { token: tenantToken });
    record(
      "separation",
      "Master and local state returned together",
      detail.status === 200 &&
        detail.json?.data?.movie?.id === createdMovie.id &&
        detail.json?.data?.local?.status === "PENDING",
    );

    const inventory = await call("/api/v1/tenant/inventory?status=PENDING", { token: tenantToken });
    const rows = inventory.json?.data ?? [];
    record(
      "separation",
      "Inventory endpoint exposes mst_movie_local rows",
      inventory.status === 200 && rows.some((row) => row.movie_id === createdMovie.id),
      `rows=${rows.length}`,
    );

    await call(`/api/v1/tenant/movies/${createdMovie.id}`, { method: "DELETE", token: tenantToken });
  }

  // The welcome layout must never carry catalog composition again.
  const layout = await call("/api/v1/tenant/layout", { token: tenantToken });
  record(
    "separation",
    "Layout document is welcome-screen only",
    layout.status === 200 && layout.json?.data?.catalog_layout === undefined,
  );
}

// ---------------------------------------------------------------------------
group("Layout & guest personalisation");
{
  // The greeting is per cabin, so the check-in must target the cabin that owns
  // the paired verification device, otherwise the payload cannot reflect it.
  const roomId = deviceRoomId;
  record("personalisation", "Verification device is bound to a cabin", Boolean(roomId), roomId);

  if (roomId) {
    const checkin = await call(`/api/v1/tenant/rooms/${roomId}/guest`, {
      method: "POST",
      token: tenantToken,
      body: { guest_name: "Ibu Siti Rahayu", guest_salutation: "Ibu" },
    });
    record("personalisation", "Check-in stores guest", checkin.status === 200 && checkin.json?.data?.guest_name === "Ibu Siti Rahayu");
    guestRoomId = roomId;

    const init = await call(`/api/v1/tv/init?device_id=${encodeURIComponent(deviceUid)}`, { deviceToken });
    const greeting = String(init.json?.data?.room?.greeting ?? "");
    record(
      "personalisation",
      "VIP greeting rendered on the TV payload",
      greeting.includes("Ibu Siti Rahayu"),
      greeting,
    );
    record(
      "personalisation",
      "Guest name and salutation present in room block",
      init.json?.data?.room?.guest_name === "Ibu Siti Rahayu" &&
        init.json?.data?.room?.guest_salutation === "Ibu",
    );

    const checkout = await call(`/api/v1/tenant/rooms/${roomId}/guest`, { method: "DELETE", token: tenantToken });
    record("personalisation", "Check-out clears guest", checkout.status === 200);

    const after = await call(`/api/v1/tv/init?device_id=${encodeURIComponent(deviceUid)}`, { deviceToken });
    record(
      "personalisation",
      "Greeting falls back after check-out",
      !String(after.json?.data?.room?.greeting ?? "").includes("Siti"),
    );
  }

  const layout = await call("/api/v1/tenant/layout", { token: tenantToken });
  const current = layout.json?.data;
  const published = await call("/api/v1/tenant/layout", {
    method: "PUT",
    token: tenantToken,
    body: {
      config: {
        ...current,
        welcome_screen: { ...current.welcome_screen, layout_variant: "FULLSCREEN_CENTER", cta_button_text: "Tonton Sekarang" },
      },
    },
  });
  record("personalisation", "Publish new layout version", published.status === 200 && published.json?.data?.version >= 2);

  const invalid = await call("/api/v1/tenant/layout", {
    method: "PUT",
    token: tenantToken,
    body: { config: { ...current, app_title: "", welcome_screen: { ...current.welcome_screen, layout_variant: "NOPE" } } },
  });
  record("personalisation", "Invalid layout rejected", invalid.status === 400 && Array.isArray(invalid.json?.errors));

  const restored = await call("/api/v1/tenant/layout", {
    method: "PUT",
    token: tenantToken,
    body: { config: { ...current, welcome_screen: { ...current.welcome_screen, layout_variant: "SPLIT_LEFT_CARD", cta_button_text: "Masuk ke Hiburan" } } },
  });
  record("personalisation", "Layout restored", restored.status === 200);
}

// ---------------------------------------------------------------------------
group("Announcements");
{
  const created = await call("/api/v1/tenant/announcements", {
    method: "POST",
    token: tenantToken,
    body: { message: "Verifikasi: kapal akan bersandar dalam 30 menit.", severity: "INFO" },
  });
  record("announcements", "Create announcement", created.status === 201);
  const id = created.json?.data?.id;

  if (id) {
    const list = await call("/api/v1/tenant/announcements", { token: tenantToken });
    record("announcements", "Announcement appears in list", (list.json?.data ?? []).some((a) => a.id === id));

    const updated = await call(`/api/v1/tenant/announcements/${id}`, {
      method: "PUT",
      token: tenantToken,
      body: { message: "Verifikasi: pesan diperbarui.", severity: "WARNING", is_active: true },
    });
    record("announcements", "Update announcement", updated.status === 200 && updated.json?.data?.severity === "WARNING");

    const deleted = await call(`/api/v1/tenant/announcements/${id}`, { method: "DELETE", token: tenantToken });
    record("announcements", "Delete announcement", deleted.status === 200);
  }
}

// ---------------------------------------------------------------------------
group("Sync queue & worker");
{
  const job = await call("/api/v1/tenant/sync-jobs", {
    method: "POST",
    token: tenantToken,
    body: { type: "MEDIA_RESCAN", payload: { reason: "verification" } },
  });
  record("sync", "Enqueue MEDIA_RESCAN", job.status === 201 && job.json?.data?.status === "QUEUED");
  const jobId = job.json?.data?.id;

  await new Promise((r) => setTimeout(r, 20_000));

  const list = await call("/api/v1/tenant/sync-jobs", { token: tenantToken });
  const row = (list.json?.data ?? []).find((j) => j.id === jobId);
  record("sync", "Worker consumed the job", row?.status === "COMPLETED", row?.status ?? "not found");

  const bad = await call("/api/v1/tenant/sync-jobs", {
    method: "POST",
    token: tenantToken,
    body: { type: "DELETE_EVERYTHING" },
  });
  record("sync", "Unknown job type rejected", bad.status === 400);
}

// ---------------------------------------------------------------------------
group("Page performance");
{
  const started = Date.now();
  for (let i = 0; i < 20; i++) {
    await call("/api/v1/tenant/devices?limit=20", { token: tenantToken });
  }
  const perRequest = (Date.now() - started) / 20;
  record("perf", "Device list stays under 200 ms (NFR)", perRequest < 200, `${perRequest.toFixed(1)} ms average`);
}

// ---------------------------------------------------------------------------
const failed = results.filter((r) => !r.ok);
console.log("\n=============================================");
console.log(`${results.length - failed.length}/${results.length} pemeriksaan lulus`);
if (failed.length > 0) {
  console.log("\nGAGAL:");
  for (const f of failed) {
    console.log(` - [${f.group}] ${f.name}${f.detail ? " :: " + f.detail : ""}`);
  }
  process.exitCode = 1;
}
