// Concurrency test for the byte-range streaming endpoint.
// Simulates N TVs seeking and streaming the same film simultaneously.
import fs from "node:fs";

const BASE = "http://127.0.0.1:8080";
const movieId = fs.readFileSync(".verify/laskar_id.txt", "utf8").trim();
const CONCURRENCY = Number(process.argv[2] ?? 50);
const CHUNKS_PER_CLIENT = Number(process.argv[3] ?? 20);

async function oneClient(index) {
  // Each client starts at a different offset, like real seeking behaviour.
  const size = 1048576;

  let bytes = 0;
  let status = 0;

  for (let round = 0; round < CHUNKS_PER_CLIENT; round++) {
    const from = (index * 4096 + round * 1024) % (size - 2048);
    const to = from + 1023;
    const res = await fetch(`${BASE}/api/v1/stream/${movieId}`, {
      headers: { Range: `bytes=${from}-${to}` },
    });
    status = res.status;
    if (res.status !== 206) {
      throw new Error(`client ${index} range ${from}-${to}: expected 206, got ${res.status}`);
    }
    const buf = await res.arrayBuffer();
    bytes += buf.byteLength;
  }

  return { index, bytes, status };
}

const started = Date.now();

const settled = await Promise.allSettled(
  Array.from({ length: CONCURRENCY }, (_, i) => oneClient(i)),
);

const elapsed = Date.now() - started;
const ok = settled.filter((s) => s.status === "fulfilled");
const failed = settled.filter((s) => s.status === "rejected");
const totalBytes = ok.reduce((sum, s) => sum + s.value.bytes, 0);

console.log(`concurrency      : ${CONCURRENCY} klien x ${CHUNKS_PER_CLIENT} chunk 1 KiB`);
console.log(`berhasil         : ${ok.length}`);
console.log(`gagal            : ${failed.length}`);
console.log(`total byte       : ${totalBytes}`);
console.log(`durasi           : ${elapsed} ms`);
console.log(`throughput       : ${(totalBytes / 1024 / 1024 / (elapsed / 1000)).toFixed(2)} MiB/s`);
if (failed.length > 0) {
  console.log("contoh error:", failed[0].reason?.message);
  process.exitCode = 1;
}
