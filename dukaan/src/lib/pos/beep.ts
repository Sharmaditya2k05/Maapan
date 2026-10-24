/** Tiny WebAudio tones for the counter: a short high beep on a good scan, a low double buzz on an unknown code. */

let ctx: AudioContext | null = null

function audio(): AudioContext | null {
  if (typeof window === 'undefined') return null
  try {
    ctx ??= new AudioContext()
    if (ctx.state === 'suspended') void ctx.resume()
    return ctx
  } catch {
    return null
  }
}

function tone(ac: AudioContext, freq: number, at: number, ms: number, gain = 0.08) {
  const o = ac.createOscillator()
  const g = ac.createGain()
  o.type = 'square'
  o.frequency.value = freq
  g.gain.setValueAtTime(gain, at)
  g.gain.exponentialRampToValueAtTime(0.0001, at + ms / 1000)
  o.connect(g).connect(ac.destination)
  o.start(at)
  o.stop(at + ms / 1000 + 0.01)
}

/** `beep(true)` for success, `beep(false)` for an error. Silently does nothing when audio is unavailable. */
export function beep(ok: boolean) {
  const ac = audio()
  if (!ac) return
  const t = ac.currentTime
  if (ok) tone(ac, 1760, t, 70)
  else {
    tone(ac, 220, t, 140, 0.1)
    tone(ac, 220, t + 0.18, 140, 0.1)
  }
}
