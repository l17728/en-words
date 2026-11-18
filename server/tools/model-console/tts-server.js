const http = require("node:http");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { execFile } = require("node:child_process");

const ROOT = path.resolve(__dirname, "..");
const HOST = "127.0.0.1";
const PORT = Number(process.env.TTS_PORT || 8002);
const MODEL_ID = "sherpa-onnx-vits-zh-ll";
const MAX_TEXT = 2000;
let active = false;
const waiters = [];
const MAX_WAITERS = 3;
const WAIT_MS = 12000;
const WORKER_MS = 6000;
const TRACE = /^[A-Za-z0-9._:-]{1,128}$/;

function busy() { return Object.assign(new Error("TTS is busy"), { status: 429 }); }
function canceled() { return Object.assign(new Error("TTS canceled"), { status: 499 }); }

function acquire(signal) {
  if (signal.aborted) return Promise.reject(canceled());
  if (!active) { active = true; return Promise.resolve(0); }
  if (waiters.length >= MAX_WAITERS) return Promise.reject(busy());
  const began = Date.now();
  return new Promise((resolve, reject) => {
    const waiter = { signal, resolve: null, reject: null };
    let settled = false;
    const cleanup = () => { clearTimeout(timer); signal.removeEventListener("abort", onAbort); };
    const fail = (error) => {
      if (settled) return;
      settled = true;
      cleanup();
      const index = waiters.indexOf(waiter);
      if (index >= 0) waiters.splice(index, 1);
      reject(error);
    };
    const onAbort = () => fail(canceled());
    const timer = setTimeout(() => fail(busy()), WAIT_MS);
    waiter.reject = fail;
    waiter.resolve = () => {
      if (settled) return false;
      settled = true;
      cleanup();
      resolve(Date.now() - began);
      return true;
    };
    waiters.push(waiter);
    signal.addEventListener("abort", onAbort, { once: true });
    if (signal.aborted) onAbort();
  });
}

function release() {
  while (waiters.length) {
    const next = waiters.shift();
    if (next.signal.aborted) { next.reject(canceled()); continue; }
    if (next.resolve()) return;
  }
  active = false;
}

function sendJson(res, status, value) {
  const body = Buffer.from(JSON.stringify(value));
  res.writeHead(status, { "Content-Type": "application/json; charset=utf-8", "Content-Length": body.length });
  res.end(body);
}

function readJson(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let size = 0;
    req.on("data", (chunk) => {
      size += chunk.length;
      if (size > 64 * 1024) req.destroy(new Error("Request is too large"));
      else chunks.push(chunk);
    });
    req.on("end", () => {
      try { resolve(JSON.parse(Buffer.concat(chunks).toString("utf8") || "{}")); }
      catch { reject(new Error("Invalid JSON body")); }
    });
    req.on("error", reject);
  });
}

function synthesize(input, voice, speed, signal) {
  return new Promise((resolve, reject) => {
    const work = fs.mkdtempSync(path.join(os.tmpdir(), "picer-tts-"));
    const textFile = path.join(work, "input.txt");
    const output = path.join(work, "output.wav");
    const helper = path.join(ROOT, "manager", "tts-utf8-worker.exe");
    const cleanup = () => {
      for (const file of [textFile, output]) try { fs.unlinkSync(file); } catch {}
      try { fs.rmdirSync(work); } catch {}
    };
    if (signal.aborted) { cleanup(); reject(new Error("TTS canceled")); return; }
    try { fs.writeFileSync(textFile, input, { encoding: "utf8", flag: "wx" }); }
    catch (error) { cleanup(); reject(error); return; }
    let child;
    const abort = () => { if (child) child.kill(); };
    try { child = execFile(helper, [textFile, output, String(voice), String(speed)], {
      cwd: ROOT, windowsHide: true, timeout: WORKER_MS, maxBuffer: 64 * 1024
    }, (error) => {
      signal.removeEventListener("abort", abort);
      try {
        // Native stderr can contain input fragments; keep API errors bounded.
        if (error) { reject(new Error(`TTS synthesis failed (exit ${error.code ?? "unknown"})`)); return; }
        if (signal.aborted) { reject(new Error("TTS canceled")); return; }
        resolve({ output, audio: fs.readFileSync(output) });
      } catch (readError) { reject(readError); }
      finally { cleanup(); }
    }); } catch (error) { cleanup(); reject(error); return; }
    signal.addEventListener("abort", abort, { once: true });
    if (signal.aborted) abort();
  });
}

const server = http.createServer(async (req, res) => {
  const url = new URL(req.url, `http://${HOST}:${PORT}`);
  if (req.method === "GET" && url.pathname === "/v1/models") {
    return sendJson(res, 200, { object: "list", data: [{ id: MODEL_ID, object: "model", owned_by: "local" }] });
  }
  if (req.method !== "POST" || url.pathname !== "/v1/audio/speech") return sendJson(res, 404, { error: "Not found" });
  const abort = new AbortController();
  req.on("aborted", () => abort.abort());
  res.on("close", () => { if (!res.writableEnded) abort.abort(); });
  let output;
  let acquired = false;
  let waitMs = 0;
  let status = 500;
  const started = Date.now();
  const trace = typeof req.headers["x-trace-id"] === "string" && TRACE.test(req.headers["x-trace-id"])
    ? req.headers["x-trace-id"] : "invalid";
  try {
    const body = await readJson(req);
    const input = String(body.input || "").trim();
    if (!input || input.length > MAX_TEXT) { status = 400; return sendJson(res, 400, { error: `input must contain 1-${MAX_TEXT} characters` }); }
    if (body.model && body.model !== MODEL_ID) { status = 400; return sendJson(res, 400, { error: "Unsupported model" }); }
    waitMs = await acquire(abort.signal);
    acquired = true;
    const voice = Math.max(0, Math.min(4, Number.isInteger(body.voice) ? body.voice : Number(body.voice) || 0));
    const speed = Math.max(0.6, Math.min(1.8, Number(body.speed) || 1));
    const result = await synthesize(input, voice, speed, abort.signal);
    output = result.output;
    if (abort.signal.aborted) { status = 499; return; }
    res.writeHead(200, { "Content-Type": "audio/wav", "Content-Length": result.audio.length, "Cache-Control": "no-store" });
    res.end(result.audio);
    status = 200;
  } catch (error) {
    status = abort.signal.aborted ? 499 : (error.status || 500);
    if (!res.headersSent && !res.destroyed) sendJson(res, status, { error: status === 429 ? "TTS is busy" : "TTS unavailable" });
  } finally {
    if (acquired) release();
    if (output) try { fs.unlinkSync(output); } catch { }
    process.stdout.write(JSON.stringify({ module: "tts_zh", event: "synthesis_completed", trace_id: trace,
      status, wait_ms: waitMs, duration_ms: Date.now() - started, pending: waiters.length }) + "\n");
  }
});

server.listen(PORT, HOST, () => process.stdout.write(`Chinese TTS ready at http://${HOST}:${PORT}\n`));
