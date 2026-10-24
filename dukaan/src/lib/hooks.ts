'use client'

/**
 * Data hooks every screen uses. They wrap TanStack Query around the gateway
 * client so pages stay small:
 *
 *   const q = useList<Invoice>('/aadesh/v1/invoices', { doc_type: 'sale', limit: 50 })
 *   const inv = useGet<Invoice>(`/aadesh/v1/invoices/${id}`)
 *   const save = useSave<Party>('/bahi/v1/parties', { invalidate: ['/bahi/v1/parties'] })
 *   await save.mutateAsync({ name: 'Sharma' })
 */
import { useCallback, useEffect, useState, useSyncExternalStore } from 'react'
import { useMutation, useQuery, useQueryClient, type UseQueryOptions } from '@tanstack/react-query'
import { api, errorMessage, get } from './api'
import { session } from './session'
import type { ListResponse, SessionUser } from './types'
import { can, type Perm } from './rbac'
import { useToast } from '@/components/ui/toast'

type Query = Record<string, string | number | boolean | undefined | null>

const clean = (q?: Query) => {
  if (!q) return undefined
  const out: Record<string, string | number> = {}
  for (const [k, v] of Object.entries(q)) if (v !== undefined && v !== null && v !== '') out[k] = typeof v === 'boolean' ? String(v) : v
  return out
}

/** GET a single resource. `enabled: false` skips the request (e.g. while an id is empty). */
export function useGet<T>(path: string | null, query?: Query, opts?: Partial<UseQueryOptions<T>>) {
  return useQuery<T>({
    queryKey: [path, clean(query)],
    queryFn: () => get<T>(path!, clean(query)),
    enabled: !!path && (opts?.enabled ?? true),
    ...opts,
  })
}

/** GET a paged list ({items,total,limit,offset}). */
export function useList<T>(path: string | null, query?: Query, opts?: Partial<UseQueryOptions<ListResponse<T>>>) {
  return useGet<ListResponse<T>>(path, query, opts)
}

/** POST/PUT/PATCH/DELETE with toast on error and cache invalidation on success. */
export function useSave<TOut = unknown, TIn = unknown>(path: string | ((input: TIn) => string), opts?: {
  method?: 'POST' | 'PUT' | 'PATCH' | 'DELETE'
  invalidate?: (string | unknown[])[]
  silent?: boolean
  onSuccess?: (out: TOut, input: TIn) => void
}) {
  const qc = useQueryClient()
  const toast = useToast()
  return useMutation<TOut, Error, TIn>({
    mutationFn: (input) => api<TOut>(typeof path === 'function' ? path(input) : path, { method: opts?.method ?? 'POST', body: opts?.method === 'DELETE' ? undefined : input }),
    onSuccess: (out, input) => {
      for (const key of opts?.invalidate ?? []) {
        if (typeof key === 'string') void qc.invalidateQueries({ predicate: (q) => String(q.queryKey[0] ?? '').startsWith(key) })
        else void qc.invalidateQueries({ queryKey: key, exact: false })
      }
      opts?.onSuccess?.(out, input)
    },
    onError: (e) => { if (!opts?.silent) toast(errorMessage(e), 'error') },
  })
}

/** Invalidate every query whose path starts with one of the given prefixes ('/bahi/v1/parties' also refreshes '/bahi/v1/parties/{id}'). */
export function useInvalidate() {
  const qc = useQueryClient()
  return useCallback((...paths: string[]) => {
    for (const p of paths) void qc.invalidateQueries({ predicate: (q) => String(q.queryKey[0] ?? '').startsWith(p) })
  }, [qc])
}

/* ---------------- session ---------------- */

const subscribe = (fn: () => void) => session.subscribe(fn)
const snapshot = () => session.user()
const serverSnapshot = () => undefined

/** The signed-in user (undefined while unknown / signed out). Re-renders on login, store switch, logout. */
export function useSessionUser(): SessionUser | undefined {
  return useSyncExternalStore(subscribe, snapshot, serverSnapshot)
}

/** Active store id and the store object; `setStore` switches and re-fetches everything. */
export function useStore() {
  const user = useSessionUser()
  const storeId = useSyncExternalStore(subscribe, () => session.storeId(), () => undefined)
  const qc = useQueryClient()
  const setStore = useCallback((id: string) => { session.setStore(id); void qc.invalidateQueries() }, [qc])
  const store = user?.stores.find((s) => s.id === storeId)
  return { storeId, store, stores: user?.stores ?? [], setStore }
}

/** `const canDo = usePerm(); canDo(PERMS.invoiceCancel)` */
export function usePerm() {
  const user = useSessionUser()
  return useCallback((perm: Perm) => can(user?.role, perm), [user?.role])
}

/** True after hydration; use to avoid SSR/CSR mismatches on session-dependent UI. */
export function useMounted() {
  const [m, setM] = useState(false)
  useEffect(() => setM(true), [])
  return m
}

/** navigator.onLine as state. */
export function useOnline() {
  const [online, setOnline] = useState(true)
  useEffect(() => {
    const up = () => setOnline(navigator.onLine)
    up()
    window.addEventListener('online', up)
    window.addEventListener('offline', up)
    return () => { window.removeEventListener('online', up); window.removeEventListener('offline', up) }
  }, [])
  return online
}

/** Debounced value (search boxes). */
export function useDebounced<T>(value: T, ms = 250) {
  const [v, setV] = useState(value)
  useEffect(() => { const t = setTimeout(() => setV(value), ms); return () => clearTimeout(t) }, [value, ms])
  return v
}

/** Keyboard shortcut (e.g. useHotkey('F2', openTenders)). Ignores typing inside inputs unless `always`. */
export function useHotkey(key: string, fn: () => void, opts?: { ctrl?: boolean; alt?: boolean; always?: boolean }) {
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if (e.key !== key) return
      if (!!opts?.ctrl !== (e.ctrlKey || e.metaKey) || !!opts?.alt !== e.altKey) return
      const t = e.target as HTMLElement | null
      if (!opts?.always && t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable) && !key.startsWith('F')) return
      e.preventDefault()
      fn()
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [key, fn, opts?.ctrl, opts?.alt, opts?.always])
}

/** Today in IST as YYYY-MM-DD. */
export function todayIST(): string {
  return new Date(Date.now() + 5.5 * 3600_000).toISOString().slice(0, 10)
}

/** First day of the current Indian financial year (1 April) as YYYY-MM-DD. */
export function fyStartIST(): string {
  const t = todayIST()
  const y = Number(t.slice(0, 4))
  const m = Number(t.slice(5, 7))
  return `${m < 4 ? y - 1 : y}-04-01`
}

/** Format a YYYY-MM-DD or RFC3339 string for Indian readers: 23 Oct 2026. */
export function fmtDate(s: string | undefined | null, withTime = false) {
  if (!s) return ''
  const d = new Date(s.length === 10 ? `${s}T00:00:00+05:30` : s)
  if (Number.isNaN(d.getTime())) return s
  return d.toLocaleString('en-IN', { timeZone: 'Asia/Kolkata', day: '2-digit', month: 'short', year: 'numeric', ...(withTime ? { hour: '2-digit', minute: '2-digit' } : {}) })
}

/** Qty with up to 3 decimals, no trailing zeros. */
export const fmtQty = (q: number | undefined | null) => (q ?? 0).toLocaleString('en-IN', { maximumFractionDigits: 3 })

/** Download a Blob/text as a file (CSV, JSON, SVG). */
export function downloadFile(name: string, data: Blob | string, type = 'text/plain') {
  const blob = data instanceof Blob ? data : new Blob([data], { type })
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}
