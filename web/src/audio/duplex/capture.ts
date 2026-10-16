import { workletSource } from './worklet-source.ts'
import { captureConstraints, capabilities } from './capabilities.ts'
import { PCMFrames, RingVad } from './pcm.ts'
import type { VadEvent } from './pcm.ts'
export type Observation = { frames: number; batches: number; event?: VadEvent; failure?: 'processor_error' | 'buffer_overflow' }
export class PreflightCapture {
  private generation = 0
  private stream?: MediaStream
  private context?: AudioContext
  private source?: MediaStreamAudioSourceNode
  private node?: AudioWorkletNode
  async start(onCapabilities: (value: ReturnType<typeof capabilities>) => void, onObservation: (value: Observation) => void, authorized: () => boolean = () => true) {
    this.stop(); const generation = this.generation
    const current = () => this.generation === generation && authorized()
    const releaseStale = () => { if (this.generation === generation) this.stop() }
    if (!current()) { releaseStale(); return }
    const stream = await navigator.mediaDevices.getUserMedia({ audio: captureConstraints })
    if (!current()) { stream.getTracks().forEach(t => t.stop()); return }
    this.stream = stream
    try {
      const track = stream.getAudioTracks()[0]
      if (!track || track.readyState !== 'live') throw new Error('Microphone unavailable')
      let available: MediaTrackCapabilities = {}
      try { available = track.getCapabilities?.() || {} } catch { /* Capability inspection is optional; do not infer effective settings. */ }
      onCapabilities(capabilities(track.getSettings(), navigator.mediaDevices.getSupportedConstraints(), available))
      if (!current()) { releaseStale(); return }
      track.onended = () => { const notify = current(); releaseStale(); if (notify) onObservation({ frames: 0, batches: 0 }) }
      const context = new AudioContext(); this.context = context
      if (!context.audioWorklet) throw new Error('AudioWorklet unavailable')

      const url = URL.createObjectURL(new Blob([workletSource], { type: 'text/javascript' }))
      try { await context.audioWorklet.addModule(url) } finally { URL.revokeObjectURL(url) }
      if (!current()) { releaseStale(); return }
      const node = new AudioWorkletNode(context, 'duplex-preflight'); this.node = node
      node.onprocessorerror = () => { const notify = current(); releaseStale(); if (notify) onObservation({ frames: 0, batches: 0, failure: 'processor_error' }) }
      const frames = new PCMFrames(context.sampleRate); const vad = new RingVad(); let count = 0
      node.port.onmessage = event => {
        if (!current()) { releaseStale(); return }
        if (event.data?.overflow) { releaseStale(); onObservation({ frames: 0, batches: 0, failure: 'buffer_overflow' }); return }
        node.port.postMessage('ack')
        for (const frame of frames.push(event.data)) {
          count++; const events = vad.push(frame)
          if (count % 4 === 0 || events.length) onObservation({ frames: count, batches: Math.floor(count / 4), event: events.at(-1) })
        }
      }
      this.source = context.createMediaStreamSource(stream); this.source.connect(node)
      // Worklet outputs silence; microphone samples are never connected to speakers.
      node.connect(context.destination)
      await context.resume()
      if (!current()) { releaseStale(); return }
    } catch (error) { releaseStale(); throw error }
  }
  stop() {
    this.generation++
    if (this.node) { this.node.onprocessorerror = null; this.node.port.onmessage = null; this.node.port.close(); this.node.disconnect(); this.node = undefined }
    this.source?.disconnect(); this.source = undefined
    this.stream?.getTracks().forEach(t => { t.onended = null; t.stop() }); this.stream = undefined
    const context = this.context; this.context = undefined; if (context) void context.close().catch(() => {})
  }
}
