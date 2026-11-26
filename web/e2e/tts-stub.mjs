import { createServer } from 'node:http'
import { readFile } from 'node:fs/promises'
import { resolve } from 'node:path'

const port = Number(process.env.E2E_TTS_STUB_PORT || 18082)
const wav = await readFile(resolve('../review/audio-fixtures/en/01.wav'))
const state = { calls: 0, fail: false, delayMs: 0, inputs: [] }
const services = [
  { id: 'vl', model: 'fixture-text', capabilities: ['text'] },
  { id: 'asr', model: 'fixture-asr', capabilities: ['asr'] },
  { id: 'tts', model: 'fixture-zh', capabilities: ['tts'] },
  { id: 'tts-alt', model: 'fixture-zh-alt', capabilities: ['tts'] },
  { id: 'tts-en', model: 'fixture-en', capabilities: ['tts'] },
]

function json(res, status, value) {
  res.writeHead(status, { 'content-type': 'application/json' })
  res.end(JSON.stringify(value))
}

const server = createServer(async (req, res) => {
  const url = new URL(req.url || '/', `http://127.0.0.1:${port}`)
  if (req.method === 'GET' && url.pathname === '/__control/health') return json(res, 200, { ok: true })
  if (req.method === 'GET' && url.pathname === '/__control/stats') return json(res, 200, { calls: state.calls, inputs: state.inputs })
  if (req.method === 'POST' && url.pathname === '/__control/reset') {
    state.calls = 0; state.fail = false; state.delayMs = 0; state.inputs = []
    return json(res, 200, { ok: true })
  }
  if (req.method === 'POST' && url.pathname === '/__control/config') {
    let body = ''
    for await (const chunk of req) body += chunk
    const config = JSON.parse(body || '{}')
    state.fail = config.fail === true
    state.delayMs = Math.max(0, Math.min(5000, Number(config.delayMs) || 0))
    return json(res, 200, { ok: true })
  }
  if (req.method === 'GET' && url.pathname === '/api/models') return json(res, 200, { services })
  if (req.method === 'POST' && ['/api/tts/speech', '/api/tts-en/speech'].includes(url.pathname)) {
    state.calls++
    let body = ''
    for await (const chunk of req) body += chunk
    try { state.inputs.push(JSON.parse(body).input) } catch { /* the stub only records valid TTS inputs */ }
    if (state.delayMs) await new Promise(resolve => setTimeout(resolve, state.delayMs))
    if (state.fail) return json(res, 503, { error: 'fixture_tts_failure' })
    res.writeHead(200, { 'content-type': 'audio/wav', 'content-length': wav.length })
    return res.end(wav)
  }
  json(res, 404, { error: 'not_found' })
})

server.listen(port, '127.0.0.1')
process.on('SIGTERM', () => server.close(() => process.exit(0)))
