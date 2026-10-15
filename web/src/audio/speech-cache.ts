// Memory only. A lease binds asynchronous media to one account and classroom.
export class SpeechCache {
  private scope = ''
  private epoch = 0
  private bytes = 0
  private entries = new Map<string, { blob: Blob; expires: number }>()
  private readonly maxBytes: number
  private readonly maxEntries: number
  private readonly ttlMs: number
  private readonly now: () => number
  constructor(maxBytes = 8 * 1024 * 1024, maxEntries = 16, ttlMs = 300000,
    now: () => number = () => performance.now()) {
    this.maxBytes = maxBytes; this.maxEntries = maxEntries; this.ttlMs = ttlMs; this.now = now
  }

  clear() {
    this.epoch++
    this.scope = ''
    this.entries.clear()
    this.bytes = 0
  }

  private remove(key: string) {
    const entry = this.entries.get(key)
    if (entry) this.bytes -= entry.blob.size
    this.entries.delete(key)
  }

  async play(scope: string, text: string, signal: AbortSignal,
    load: () => Promise<Blob>, play: (blob: Blob) => Promise<void>, check: () => void): Promise<boolean> {
    check()
    if (signal.aborted) throw new DOMException('Aborted', 'AbortError')
    if (this.scope !== scope) { this.clear(); this.scope = scope }
    const epoch = this.epoch
    const valid = () => {
      check()
      if (signal.aborted || this.epoch !== epoch || this.scope !== scope) {
        throw new DOMException('Aborted', 'AbortError')
      }
    }
    for (const [key, entry] of this.entries) if (entry.expires <= this.now()) this.remove(key)
    const cached = this.entries.get(text)
    const blob = cached ? cached.blob : await load()
    const produced = this.now()
    valid()
    // Playback validates decodability; container validation prevents caching truncated media.
    const cacheable = cached || (blob.size <= this.maxBytes && await completeWav(blob))
    valid()
    try {
      await play(blob)
      valid()
    } catch (error) {
      if (this.epoch === epoch) this.remove(text)
      throw error
    }
    if (cached && this.entries.get(text) === cached) {
      this.entries.delete(text)
      this.entries.set(text, cached) // LRU touch, never extend the original TTL.
    } else if (!cached && cacheable && produced + this.ttlMs > this.now()) {
      this.remove(text)
      while (this.entries.size && (this.entries.size >= this.maxEntries || this.bytes + blob.size > this.maxBytes)) {
        this.remove(this.entries.keys().next().value!)
      }
      if (this.maxEntries > 0 && blob.size <= this.maxBytes) {
        this.entries.set(text, { blob, expires: produced + this.ttlMs })
        this.bytes += blob.size
      }
    }
    return !!cached
  }
}

export async function completeWav(blob: Blob): Promise<boolean> {
  if (blob.type.split(';')[0].trim().toLowerCase() !== 'audio/wav' || blob.size < 44) return false
  const data = new DataView(await blob.arrayBuffer())
  const label = (offset: number) => String.fromCharCode(...Array.from({ length: 4 }, (_, i) => data.getUint8(offset + i)))
  if (label(0) !== 'RIFF' || label(8) !== 'WAVE' || data.getUint32(4, true) + 8 !== data.byteLength) return false
  let formatSeen = false, dataSeen = false, alignment = 0, audioBytes = 0
  for (let offset = 12; offset < data.byteLength;) {
    if (data.byteLength - offset < 8) return false
    const size = data.getUint32(offset + 4, true), body = offset + 8, end = body + size
    if (end > data.byteLength) return false
    const kind = label(offset)
    if (kind === 'fmt ') {
      if (formatSeen || size < 16) return false
      formatSeen = true
      const format = data.getUint16(body, true), channels = data.getUint16(body + 2, true)
      const rate = data.getUint32(body + 4, true), byteRate = data.getUint32(body + 8, true)
      alignment = data.getUint16(body + 12, true)
      const bits = data.getUint16(body + 14, true)
      if (channels < 1 || channels > 8 || rate < 8000 || rate > 192000 ||
        !((format === 1 && [8, 16, 24, 32].includes(bits)) || (format === 3 && [32, 64].includes(bits))) ||
        alignment !== channels * bits / 8 || byteRate !== rate * alignment) return false
    } else if (kind === 'data') {
      if (dataSeen || size === 0) return false
      dataSeen = true; audioBytes = size
    }
    offset = end + size % 2
    if (offset > data.byteLength) return false
  }
  return formatSeen && dataSeen && alignment > 0 && audioBytes % alignment === 0
}

export const speechCache = new SpeechCache()
