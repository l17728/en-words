// Isolated adapter for zh_tts_resident_worker.cpp. Default port intentionally differs from production.
"use strict";
const http = require("node:http");
const crypto = require("node:crypto");
const { spawn } = require("node:child_process");
const path = require("node:path");
const { TextEncoder } = require("node:util");
const HOST = "127.0.0.1", PORT = Number(process.env.ZH_TTS_ISOLATED_PORT || 18002);
const WORKER = process.env.ZH_TTS_ISOLATED_WORKER || path.resolve(__dirname, "../../.test-artifacts/zh-tts-resident/zh_tts_resident_worker.exe");
const MODEL = "sherpa-onnx-vits-zh-ll", COUNT = Math.min(4, Math.max(1, Number(process.env.ZH_TTS_WORKERS || 4)));
const MAX_FRAME = 8 * 1024 * 1024, TIMEOUT = 6000;
const slots = Array.from({ length: COUNT }, (_, index) => ({ index, child: null, ready: false, dying: false, busy: null, bytes: Buffer.alloc(0), nextId: 1, restarting: false, failures: 0, startedAt: 0, startup: null }));
const pending = [];
let stopping = false;
const encoder = new TextEncoder();
function log(event, fields = {}) { console.error(JSON.stringify({ module: "zh_tts_resident", event, ...fields })); }
function reply(res, status, object) { if (!res.writableEnded && !res.destroyed) { const b = Buffer.from(JSON.stringify(object)); res.writeHead(status, { "content-type": "application/json", "content-length": b.length, connection: "close" }); res.end(b); } }
function finish(job, error, audio, keepWorkerTimer = false) { if (job.done) return; job.done = true; if (!keepWorkerTimer) clearTimeout(job.timer); clearTimeout(job.waitTimer); job.signal.removeEventListener("abort", job.cancel); error ? job.reject(error) : job.resolve(audio); }
function failSlot(slot, error) {
  if (slot.dying) return;
  log("worker_failed", { slot: slot.index, reason: String(error?.message || "failure"), request_id: slot.busy?.id?.toString() || null });
  slot.ready = false; slot.dying = true; clearTimeout(slot.startup);
  if (slot.busy) { clearTimeout(slot.busy.timer); finish(slot.busy, error); slot.busy = null; }
  if (slot.child && !slot.child.killed) slot.child.kill("SIGKILL");
  pump();
}
function validWav(b) {
  return b.length >= 45 && b.toString("ascii", 0, 4) === "RIFF" && b.toString("ascii", 8, 12) === "WAVE" &&
    b.toString("ascii", 12, 16) === "fmt " && b.readUInt16LE(20) === 1 && b.readUInt16LE(22) === 1 &&
    b.readUInt32LE(24) === 16000 && b.readUInt32LE(28) === 32000 && b.readUInt16LE(32) === 2 && b.readUInt16LE(34) === 16 &&
    b.toString("ascii", 36, 40) === "data" &&
    b.readUInt32LE(40) === b.length - 44 && b.readUInt32LE(4) === b.length - 8;
}
function parse(slot, chunk) {
  if (slot.dying) return;
  slot.bytes = Buffer.concat([slot.bytes, chunk]);
  if (slot.bytes.length > MAX_FRAME + 20) throw Error("frame_oversize");
  while (slot.bytes.length >= 20) {
    const h = slot.bytes;
    if (h.toString("ascii", 0, 4) !== "EWTS" || h[4] !== 1 || h.readUInt16LE(6) !== 0) throw Error("bad_header");
    const n = h.readUInt32LE(16);
    if (n > MAX_FRAME || (h[5] === 2 && n !== 2) || (h[5] === 4 && n !== 2)) throw Error("bad_length");
    if (slot.bytes.length < 20 + n) return;
    const id = h.readBigUInt64LE(8), kind = h[5], body = slot.bytes.subarray(20, 20 + n);
    slot.bytes = slot.bytes.subarray(20 + n);
    if (kind === 2 && !slot.ready && id === 0n && !slot.busy && body[0] === slot.index && body[1] === 1) { clearTimeout(slot.startup); slot.ready = true; log("worker_ready", { slot: slot.index, pid: slot.child?.pid }); pump(); continue; }
    const job = slot.busy;
    if (!job || id !== job.id || (kind !== 3 && kind !== 4)) throw Error("wrong_response");
    if (kind === 3 && !validWav(body)) throw Error("bad_wav");
    slot.busy = null; clearTimeout(job.timer); log("synth_finished", { slot: slot.index, request_id: id.toString(), trace_id: job.trace, status: kind === 3 ? "ok" : "failed", duration_ms: Date.now() - job.startedAt }); finish(job, kind === 4 ? Error("synth_failed") : null, kind === 3 ? Buffer.from(body) : null); pump();
  }
}
function start(slot) {
  if (stopping || slot.child || slot.failures >= 3) return;
  const child = spawn(WORKER, ["--serve", String(slot.index), String(process.pid)], { stdio: ["pipe", "pipe", "ignore"], windowsHide: true });
  slot.child = child; slot.ready = false; slot.dying = false; slot.bytes = Buffer.alloc(0); slot.nextId = 1; slot.startedAt = Date.now();
  slot.startup = setTimeout(() => failSlot(slot, Error("startup_timeout")), 10000);
  child.stdout.on("data", b => { try { parse(slot, b); } catch (e) { failSlot(slot, e); } });
  child.on("error", e => failSlot(slot, e));
  child.on("close", () => {
    if (slot.child !== child) return;
    clearTimeout(slot.startup);
    slot.child = null; slot.ready = false; slot.dying = false; slot.bytes = Buffer.alloc(0);
    log("worker_exited", { slot: slot.index, pid: child.pid });
    if (slot.busy) { clearTimeout(slot.busy.timer); finish(slot.busy, Error("worker_exited")); slot.busy = null; }
    pump();
    if (Date.now() - slot.startedAt > 60000) slot.failures = 0;
    if (!stopping && !slot.restarting && ++slot.failures < 3) { slot.restarting = true; setTimeout(() => { slot.restarting = false; start(slot); }, 300 * slot.failures); }
  });
}
function pump() {
  for (const slot of slots) {
    if (!slot.ready || slot.busy) continue;
    while (pending.length) {
      const job = pending.shift();
      if (job.done || job.signal.aborted) { finish(job, Error("canceled")); continue; }
      slot.busy = job; job.id = BigInt(slot.nextId++); clearTimeout(job.waitTimer); job.startedAt = Date.now();
      log("synth_started", { slot: slot.index, request_id: job.id.toString(), trace_id: job.trace, queue_ms: job.startedAt - job.enqueuedAt });
      const text = Buffer.from(encoder.encode(job.input));
      const body = Buffer.alloc(5 + text.length);
      body[0] = job.sid; body.writeUInt16LE(job.speed, 1); body.writeUInt16LE(text.length, 3); text.copy(body, 5);
      const h = Buffer.alloc(20); h.write("EWTS", 0, "ascii"); h[4] = 1; h[5] = 1; h.writeBigUInt64LE(job.id, 8); h.writeUInt32LE(body.length, 16);
      job.timer = setTimeout(() => failSlot(slot, Error("worker_timeout")), TIMEOUT);
      slot.child.stdin.write(Buffer.concat([h, body]), e => { if (e) failSlot(slot, e); });
      break;
    }
  }
}
function submit(input, sid, speed, signal, trace) {
  return new Promise((resolve, reject) => {
    const capacity = slots.filter(x => x.ready).length * 2;
    if (!capacity) return reject(Object.assign(Error("not_ready"), { status: 503 }));
    if (pending.length + slots.filter(x => x.busy).length >= capacity) return reject(Object.assign(Error("busy"), { status: 429 }));
    const job = { input, sid, speed, signal, trace, enqueuedAt: Date.now(), resolve, reject, done: false, timer: null, waitTimer: null, id: 0n, cancel: null };
    job.cancel = () => { if (!job.id) { const i = pending.indexOf(job); if (i >= 0) pending.splice(i, 1); } log("synth_canceled", { trace_id: job.trace, request_id: job.id ? job.id.toString() : null }); finish(job, Error("canceled"), null, !!job.id); };
    signal.addEventListener("abort", job.cancel, { once: true });
    pending.push(job);
    job.waitTimer = setTimeout(() => { if (!job.id && !job.done) { const i = pending.indexOf(job); if (i >= 0) pending.splice(i, 1); finish(job, Object.assign(Error("busy"), { status: 429 })); } }, 12000);
    if (signal.aborted) job.cancel(); pump();
  });
}
const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, `http://${HOST}:${PORT}`);
  if (req.method === "GET" && url.pathname === "/v1/models") { const ready = slots.filter(s => s.ready).length; return reply(res, ready ? 200 : 503, { object: "list", data: ready ? [{ id: MODEL, object: "model" }] : [], ready_workers: ready, target_workers: COUNT, degraded: ready < COUNT }); }
  if (req.method !== "POST" || url.pathname !== "/v1/audio/speech") return reply(res, 404, { error: "not_found" });
  const abort = new AbortController(); res.on("close", () => { if (!res.writableEnded) abort.abort(); });
  try {
    const parts = []; let size = 0;
    for await (const b of req) { size += b.length; if (size > 65536) throw Object.assign(Error("too_large"), { status: 400 }); parts.push(b); }
    let body;
    try { body = JSON.parse(Buffer.concat(parts).toString("utf8")); }
    catch { throw Object.assign(Error("invalid_input"), { status: 400 }); }
    if (!body || typeof body !== "object" || Array.isArray(body)) throw Object.assign(Error("invalid_input"), { status: 400 });
    const input = body.input, sid = body.voice === undefined ? 0 : Number(body.voice), speed = body.speed === undefined ? 1 : Number(body.speed);
    if (typeof input !== "string" || !input.length || input.length > 2000 || Buffer.byteLength(input, "utf8") > 8000 || input.includes("\0") ||
        !Number.isInteger(sid) || sid < 0 || sid > 4 || !Number.isFinite(speed) || speed < 0.6 || speed > 1.8 ||
        (body.model && body.model !== MODEL)) throw Object.assign(Error("invalid_input"), { status: 400 });
    const incoming = req.headers["x-trace-id"];
    const trace = typeof incoming === "string" && /^[A-Za-z0-9._:-]{1,128}$/.test(incoming) ? incoming : crypto.randomUUID();
    const wav = await submit(input, sid, Math.round(speed * 1000), abort.signal, trace);
    if (!res.writableEnded && !res.destroyed) { res.writeHead(200, { "content-type": "audio/wav", "content-length": wav.length, connection: "close" }); res.end(wav); }
  } catch (e) { const status = e.status || 503; reply(res, status, { error: status === 400 ? "invalid_input" : status === 429 ? "busy" : "unavailable" }); }
});
server.listen(PORT, HOST, () => { for (const slot of slots) start(slot); });
process.on("SIGINT", () => { stopping = true; for (const s of slots) s.child?.kill("SIGKILL"); server.close(); });
process.on("SIGTERM", () => { stopping = true; for (const s of slots) s.child?.kill("SIGKILL"); server.close(); });
