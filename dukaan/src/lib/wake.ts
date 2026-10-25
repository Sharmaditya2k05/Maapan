import { API_URL } from '@/lib/api'

/** Demo deployments run on hosting that puts idle services to sleep. */
export const DEMO = process.env.NEXT_PUBLIC_APP_ENV === 'local'

export interface WakeResult { ready: boolean; services: Record<string, string> }

/**
 * Asks the gateway to wake every service (GET /wake, no token). It answers
 * once all services replied, which can take up to a minute after a quiet spell.
 */
export async function wakeServers(signal?: AbortSignal): Promise<WakeResult> {
  const base = API_URL.replace(/\/api$/, '')
  const res = await fetch(`${base}/wake`, { signal, cache: 'no-store' })
  if (!res.ok) throw new Error(`wake failed (${res.status})`)
  return (await res.json()) as WakeResult
}
