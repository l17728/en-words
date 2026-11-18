// Encode decoded samples, not the compressed MediaRecorder container.
export function encodePCM16Wav(audio: Pick<AudioBuffer, 'length' | 'sampleRate' | 'numberOfChannels' | 'getChannelData'>): Blob {
  const { length, sampleRate, numberOfChannels } = audio
  if (!Number.isSafeInteger(length) || length <= 0 || !Number.isSafeInteger(sampleRate) || sampleRate !== 16000 ||
      !Number.isSafeInteger(numberOfChannels) || numberOfChannels < 1 || numberOfChannels > 8) {
    throw new Error('录音解码格式无效，请重试或使用文字输入')
  }
  const channels = Array.from({ length: numberOfChannels }, (_, channel) => audio.getChannelData(channel))
  if (channels.some(channel => channel.length !== length)) throw new Error('录音采样不完整')
  const dataBytes = length * 2
  const buffer = new ArrayBuffer(44 + dataBytes)
  const view = new DataView(buffer)
  const label = (offset: number, text: string) => {
    for (let i = 0; i < text.length; i++) view.setUint8(offset + i, text.charCodeAt(i))
  }
  label(0, 'RIFF'); view.setUint32(4, 36 + dataBytes, true); label(8, 'WAVE')
  label(12, 'fmt '); view.setUint32(16, 16, true); view.setUint16(20, 1, true)
  view.setUint16(22, 1, true); view.setUint32(24, sampleRate, true)
  view.setUint32(28, sampleRate * 2, true); view.setUint16(32, 2, true); view.setUint16(34, 16, true)
  label(36, 'data'); view.setUint32(40, dataBytes, true)
  for (let frame = 0; frame < length; frame++) {
    let sample = 0
    for (const channel of channels) {
      if (!Number.isFinite(channel[frame])) throw new Error('录音包含无效采样')
      sample += channel[frame] / numberOfChannels
    }
    sample = Math.max(-1, Math.min(1, sample))
    view.setInt16(44 + frame * 2, Math.round(sample * (sample < 0 ? 32768 : 32767)), true)
  }
  return new Blob([buffer], { type: 'audio/wav' })
}

export async function recordingToWav(audio: Blob, check: () => void): Promise<Blob> {
  check()
  const encoded = await audio.arrayBuffer()
  check()
  // This context never connects to speakers and requires no playback activation.
  const decoder = new OfflineAudioContext(1, 1, 16000)
  const decoded = await decoder.decodeAudioData(encoded)
  check()
  return encodePCM16Wav(decoded)
}
