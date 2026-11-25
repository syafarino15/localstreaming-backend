// End-to-end websocket verification for the DLU X ALFATEVE device hub.
// Run with: node .verify/ws-check.mjs
//
// Requires full-check.mjs to have run first: it pairs a device and persists the
// live credential plus its cabin in .verify/devtoken.txt and .verify/dev.json.
import fs from "node:fs";

const BASE = "http://127.0.0.1:8080";
const deviceToken = fs.readFileSync(".verify/devtoken.txt", "utf8").trim();
const adminToken = fs.readFileSync(".verify/token.txt", "utf8").trim();
const devInfo = JSON.parse(fs.readFileSync(".verify/dev.json", "utf8"));
const pairedUid = String(devInfo.device_uid ?? "");
const pairedRoomId = String(devInfo.room_id ?? "");

const results = [];

function record(name, ok, detail) {
  results.push({ name, ok, detail });
  console.log(`${ok ? "PASS" : "FAIL"}  ${name}${detail ? " — " + detail : ""}`);
}

async function api(path, options = {}) {
  const res = await fetch(BASE + path, {
    ...options,
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${adminToken}`,
      ...(options.headers ?? {}),
    },
  });
  const text = await res.text();
  return { status: res.status, body: text ? JSON.parse(text) : null };
}

// --- 1. Connect the TV socket ------------------------------------------------
const ws = new WebSocket(`ws://127.0.0.1:8080/ws/tv?token=${deviceToken}`);
const inbox = [];
let welcome = null;

const welcomePromise = new Promise((resolve, reject) => {
  const timer = setTimeout(() => reject(new Error("WELCOME timeout")), 8000);
  ws.addEventListener("message", (event) => {
    const msg = JSON.parse(event.data);
    inbox.push(msg);
    if (msg.type === "WELCOME") {
      clearTimeout(timer);
      welcome = msg;
      resolve(msg);
    }
  });
  ws.addEventListener("error", () => {
    clearTimeout(timer);
    reject(new Error("websocket error"));
  });
});

await new Promise((resolve, reject) => {
  ws.addEventListener("open", resolve, { once: true });
  ws.addEventListener("error", reject, { once: true });
});

welcome = await welcomePromise;
record("TV menerima WELCOME", Boolean(welcome?.payload?.device_id), JSON.stringify(welcome?.payload ?? {}));

// --- 2. Heartbeat ------------------------------------------------------------
ws.send(JSON.stringify({ type: "HEARTBEAT", payload: { app_version: "1.0.0-test", state: "CATALOG" } }));
await new Promise((r) => setTimeout(r, 600));

// --- 3. Devices must be ONLINE now ------------------------------------------
const devices = await api("/api/v1/tenant/devices?status=ONLINE&limit=50");
const online = devices.body?.data ?? [];
const target = online.find((d) => d.device_uid === pairedUid);
record("Perangkat ONLINE setelah socket terhubung", Boolean(target), target ? target.id : "tidak ditemukan");

const deviceId = target?.id;

// --- 4. Admin sends a remote command ----------------------------------------
let commandId = null;
if (deviceId) {
  const sent = await api(`/api/v1/tenant/devices/${deviceId}/commands`, {
    method: "POST",
    body: JSON.stringify({ command: "REFRESH_CONFIG", payload: { reason: "e2e" } }),
  });
  commandId = sent.body?.data?.id;
  record("Perintah REFRESH_CONFIG dibuat", sent.status === 201, `id=${commandId}`);

  const received = await waitFor((m) => m.type === "COMMAND" && m.payload?.id === commandId, 8000);
  record("TV menerima COMMAND via WebSocket", Boolean(received), JSON.stringify(received?.payload ?? {}));

  if (commandId) {
    ws.send(JSON.stringify({ type: "ACK", payload: { command_id: commandId } }));
    await new Promise((r) => setTimeout(r, 800));

    const history = await api("/api/v1/tenant/commands?limit=5");
    const row = (history.body?.data ?? []).find((c) => c.id === commandId);
    record("Command berstatus ACKED di database", row?.status === "ACKED", `status=${row?.status}`);
  }
}

// --- 5. VIP guest check-in pushes to the cabin TV ---------------------------
// The socket belongs to the cabin paired in full-check, so the push must target
// that cabin; otherwise the greeting has nowhere to land.
const rooms = await api("/api/v1/tenant/rooms?limit=50");
const room = (rooms.body?.data ?? []).find((r) => r.id === pairedRoomId);
record("Kamar perangkat terpasang ditemukan", Boolean(room), room?.room_number ?? "tidak ditemukan");

if (room) {
  const checkin = await api(`/api/v1/tenant/rooms/${room.id}/guest`, {
    method: "POST",
    body: JSON.stringify({ guest_name: "Hendra Wijaya", guest_salutation: "Bpk." }),
  });
  record("Check-in tamu VIP tersimpan", checkin.status === 200, `status=${checkin.status}`);

  const push = await waitFor((m) => m.type === "ROOM_GUEST_UPDATED", 8000);
  record("TV menerima ROOM_GUEST_UPDATED", Boolean(push), JSON.stringify(push?.payload ?? {}));
  if (push) {
    record(
      "Greeting berisi nama tamu",
      String(push.payload?.greeting ?? "").includes("Hendra Wijaya"),
      push.payload?.greeting,
    );
  }

  // The init payload must also reflect the new guest immediately.
  const init = await fetch(BASE + "/api/v1/tv/init", {
    headers: { "X-Device-Token": deviceToken },
  }).then((r) => r.json());
  record(
    "GET /tv/init memuat greeting tamu baru",
    String(init?.data?.room?.greeting ?? "").includes("Hendra Wijaya"),
    init?.data?.room?.greeting,
  );

  // Reset so the demo state stays clean.
  await api(`/api/v1/tenant/rooms/${room.id}/guest`, { method: "DELETE" });
}

ws.close();

// --- summary ----------------------------------------------------------------
const failed = results.filter((r) => !r.ok);
console.log("\n=== SUMMARY ===");
console.log(`${results.length - failed.length}/${results.length} pemeriksaan lulus`);
if (failed.length > 0) {
  console.log("GAGAL:", failed.map((f) => f.name).join(", "));
  process.exitCode = 1;
}

async function waitFor(predicate, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const found = inbox.find(predicate);
    if (found) return found;
    await new Promise((r) => setTimeout(r, 100));
  }
  return null;
}
