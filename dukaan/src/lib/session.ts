/**
 * Token + user storage.
 *
 * MVP: the refresh token lives in memory and localStorage. TODO (see README): move it to an
 * httpOnly, Secure, SameSite=Strict cookie set by a Next route handler (/api/session) so
 * page scripts can never read it.
 */
import type { SessionUser, TokenPair } from './types'

const KEY = 'maapan.session.v1'

interface Stored { access?: string; refresh?: string; user?: SessionUser; storeId?: string }

let mem: Stored | null = null

/** Chosen store, else the token's store (sid), else the first accessible store. */
function activeStore(s: Stored): string | undefined {
  return s.storeId ?? s.user?.default_store_id ?? s.user?.stores?.[0]?.id
}
const listeners = new Set<() => void>()

function load(): Stored {
  if (mem) return mem
  if (typeof window === 'undefined') return {}
  try { mem = JSON.parse(localStorage.getItem(KEY) || '{}') } catch { mem = {} }
  return mem!
}

function save(next: Stored) {
  mem = next
  try { localStorage.setItem(KEY, JSON.stringify(next)) } catch { /* private mode: memory only */ }
  listeners.forEach((l) => l())
}

export const session = {
  accessToken: () => load().access,
  refreshToken: () => load().refresh,
  user: () => load().user,
  storeId: () => activeStore(load()),
  store: () => {
    const s = load()
    const id = activeStore(s)
    return s.user?.stores?.find((x) => x.id === id)
  },
  setTokens: (t: TokenPair) => save({ ...load(), access: t.access_token, refresh: t.refresh_token }),
  setUser: (u: SessionUser) => {
    const cur = load().storeId
    // Keep the chosen store only while the user can still access it.
    const keep = cur && u.stores.some((s) => s.id === cur) ? cur : undefined
    save({ ...load(), user: u, storeId: keep ?? u.default_store_id ?? u.stores?.[0]?.id })
  },
  setStore: (id: string) => save({ ...load(), storeId: id }),
  clear: () => save({}),
  subscribe: (fn: () => void) => { listeners.add(fn); return () => { listeners.delete(fn) } },
}

/** Stable per-browser device id, used for offline number blocks. */
export function deviceId(): string {
  if (typeof window === 'undefined') return 'server'
  const k = 'maapan.device'
  let id = localStorage.getItem(k)
  if (!id) {
    id = `pos-${crypto.randomUUID().slice(0, 8)}`
    localStorage.setItem(k, id)
  }
  return id
}
