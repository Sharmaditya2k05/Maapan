/**
 * The only HTTP client in dukaan. Everything goes through the dwaar gateway:
 *   ${NEXT_PUBLIC_API_URL}/<service>/<path>   e.g. /aadesh/v1/invoices
 * Adds the bearer token and X-Store-ID, refreshes once on 401, and turns the
 * {"error":{code,message}} envelope into ApiError.
 */
import { session } from './session'

export const API_URL = (process.env.NEXT_PUBLIC_API_URL || 'http://localhost:8000/api').replace(/\/$/, '')

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string, public details?: unknown) {
    super(message)
  }
  /** True when the request never reached the server (offline, DNS, CORS). */
  get isNetwork() { return this.status === 0 }
}

type Opts = Omit<RequestInit, 'body'> & {
  body?: unknown
  auth?: boolean
  query?: Record<string, string | number | undefined | null>
  /** Return the response body as a Blob (CSV/SVG downloads) instead of parsed JSON. */
  raw?: boolean
}

let refreshing: Promise<boolean> | null = null

async function refresh(): Promise<boolean> {
  const rt = session.refreshToken()
  if (!rt) return false
  refreshing ??= (async () => {
    try {
      const res = await fetch(`${API_URL}/pehchaan/v1/auth/refresh`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ refresh_token: rt }),
      })
      if (!res.ok) return false
      session.setTokens(await res.json())
      return true
    } catch {
      return false
    } finally {
      setTimeout(() => { refreshing = null }, 0)
    }
  })()
  return refreshing
}

export async function api<T = unknown>(path: string, opts: Opts = {}): Promise<T> {
  const { body, auth = true, query, headers, raw = false, ...rest } = opts
  let url = `${API_URL}${path.startsWith('/') ? path : `/${path}`}`
  if (query) {
    const qs = new URLSearchParams()
    for (const [k, v] of Object.entries(query)) if (v !== undefined && v !== null && v !== '') qs.set(k, String(v))
    const s = qs.toString()
    if (s) url += `?${s}`
  }
  const isForm = typeof FormData !== 'undefined' && body instanceof FormData

  const send = () => {
    const h = new Headers(headers)
    if (body !== undefined && !isForm) h.set('Content-Type', 'application/json')
    if (auth) {
      const tok = session.accessToken()
      if (tok) h.set('Authorization', `Bearer ${tok}`)
      const store = session.storeId()
      if (store) h.set('X-Store-ID', store)
    }
    return fetch(url, { ...rest, headers: h, body: body === undefined ? undefined : isForm ? (body as FormData) : JSON.stringify(body) })
  }

  let res: Response
  try {
    res = await send()
    if (res.status === 401 && auth && (await refresh())) res = await send()
  } catch (e) {
    throw new ApiError(0, 'network', (e as Error).message || 'Network error')
  }
  if (res.status === 401 && auth) {
    session.clear()
    if (typeof window !== 'undefined' && !location.pathname.startsWith('/login')) location.href = '/login'
  }
  if (res.status === 204) return undefined as T
  if (raw && res.ok) return (await res.blob()) as T
  const text = await res.text()
  const data = text ? safeJSON(text) : undefined
  if (!res.ok) {
    const err = (data as { error?: { code?: string; message?: string; details?: unknown } } | undefined)?.error
    throw new ApiError(res.status, err?.code ?? 'http_error', err?.message ?? `Request failed (${res.status})`, err?.details)
  }
  return data as T
}

function safeJSON(t: string) {
  try { return JSON.parse(t) } catch { return t }
}

export const get = <T,>(path: string, query?: Opts['query']) => api<T>(path, { method: 'GET', query })
export const post = <T,>(path: string, body?: unknown) => api<T>(path, { method: 'POST', body })
export const put = <T,>(path: string, body?: unknown) => api<T>(path, { method: 'PUT', body })
export const del = <T,>(path: string) => api<T>(path, { method: 'DELETE' })

export const errorMessage = (e: unknown) =>
  e instanceof ApiError ? (e.isNetwork ? 'You appear to be offline.' : e.message) : (e as Error)?.message ?? 'Something went wrong'
