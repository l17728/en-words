// UUIDs correlate requests and deduplicate submissions; never authentication credentials.
export function clientId(source: Pick<Crypto, 'getRandomValues'> & Partial<Pick<Crypto, 'randomUUID'>> = globalThis.crypto): string {
  if (typeof source?.randomUUID === 'function') {
    try { return source.randomUUID() } catch { /* Fall back to secure random bytes. */ }
  }
  if (typeof source?.getRandomValues !== 'function') throw new Error('浏览器缺少安全随机能力，请更新浏览器后重试')
  const bytes = source.getRandomValues(new Uint8Array(16))
  bytes[6] = (bytes[6] & 15) | 64
  bytes[8] = (bytes[8] & 63) | 128
  const hex = Array.from(bytes, value => value.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0,8)}-${hex.slice(8,12)}-${hex.slice(12,16)}-${hex.slice(16,20)}-${hex.slice(20)}`
}
