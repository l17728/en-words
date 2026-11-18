export const captureConstraints = { echoCancellation: { ideal: true }, noiseSuppression: { ideal: true }, autoGainControl: { ideal: true } } as const
export type CapabilityState = 'enabled' | 'disabled' | 'unknown'
export function capabilities(settings: MediaTrackSettings, supported: MediaTrackSupportedConstraints = {}, available: MediaTrackCapabilities = {}) {
  return (['echoCancellation', 'noiseSuppression', 'autoGainControl'] as const).map(key => ({
    key, requested: true, available: Array.isArray(available[key]) ? (available[key] as unknown[]).filter((value): value is boolean => typeof value === 'boolean') : null, supported: supported[key] === true,
    effective: (typeof settings[key] === 'boolean' ? settings[key] ? 'enabled' : 'disabled' : 'unknown') as CapabilityState,
  }))
}
