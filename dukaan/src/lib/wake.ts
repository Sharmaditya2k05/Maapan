import { API_URL, setWakeHook } from '@/lib/api'

/** Demo deployments run on hosting that puts idle services to sleep. */
export const DEMO = process.env.NEXT_PUBLIC_APP_ENV === 'local'

export interface WakeResult { ready: boolean; services: Record<string, string> }

const SERVICES = ['pehchaan', 'aadesh', 'bahi', 'kar', 'lenden', 'soochna', 'aayaat', 'bhandar', 'nirmaan', 'buddhi']

/**
 * Public addresses of the services, derived from the gateway's host when it
 * follows the "<prefix>dwaar<suffix>" naming (e.g. maapan-dwaar.onrender.com).
 * Render wakes a sleeping free service only on traffic from outside Render:
 * calls from the gateway get "502 no-deploy" until then, so the browser wakes
 * them directly.
 */
export function serviceOrigins(): string[] {
  try {
    const u = new URL(API_URL)
    if (!u.hostname.includes('dwaar')) return []
    return SERVICES.map((s) => `${u.protocol}//${u.hostname.replace('dwaar', s)}`)
  } catch {
    return []
  }
}

/** Fire-and-forget request to every service's /healthz (opaque, no CORS needed). */
export function pokeServices(): void {
  for (const origin of serviceOrigins()) {
    void fetch(`${origin}/healthz`, { mode: 'no-cors', cache: 'no-store' }).catch(() => undefined)
  }
}

let lastPoke = 0
// Retried requests wake the services too (see sendWaking in api.ts).
if (DEMO) setWakeHook(() => pokeServicesThrottled())

/** pokeServices at most once every 20 seconds. */
export function pokeServicesThrottled(): void {
  if (Date.now() - lastPoke < 20_000) return
  lastPoke = Date.now()
  pokeServices()
}

async function gatewayWake(signal?: AbortSignal): Promise<WakeResult> {
  const base = API_URL.replace(/\/api$/, '')
  const res = await fetch(`${base}/wake`, { signal, cache: 'no-store' })
  if (!res.ok) throw new Error(`wake failed (${res.status})`)
  return (await res.json()) as WakeResult
}

/**
 * Wakes every service and resolves once the gateway sees them all up (or
 * after about 90 seconds with the last status).
 */
export async function wakeServers(signal?: AbortSignal): Promise<WakeResult> {
  const deadline = Date.now() + 90_000
  let last: WakeResult = { ready: false, services: {} }
  for (;;) {
    pokeServices()
    try {
      last = await gatewayWake(signal)
      if (last.ready) return last
    } catch (e) {
      if (signal?.aborted) throw e
    }
    if (Date.now() > deadline) return last
    await new Promise((r) => setTimeout(r, 4000))
  }
}
