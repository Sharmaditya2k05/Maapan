'use client'

/** Small React helpers shared by the stock and settings screens. */
import { useEffect, useMemo, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { api, errorMessage } from '@/lib/api'
import { useList } from '@/lib/hooks'
import { useToast } from '@/components/ui/toast'
import type { ItemFull } from '@/lib/types.stock'

/**
 * Like useSave, but you build the request: path from the input, custom method, no body.
 * Needed because the servers reject unknown JSON keys, so an `id` cannot ride in the body.
 */
export function useAction<TOut = unknown, TIn = void>(run: (input: TIn) => Promise<TOut>, opts?: {
  invalidate?: string[]
  silent?: boolean
  onSuccess?: (out: TOut, input: TIn) => void
}) {
  const qc = useQueryClient()
  const toast = useToast()
  return useMutation<TOut, Error, TIn>({
    mutationFn: run,
    onSuccess: (out, input) => {
      // Same prefix rule as useSave / useInvalidate: '/bhandar/v1/items' also refreshes '/bhandar/v1/items/{id}'.
      for (const key of opts?.invalidate ?? []) void qc.invalidateQueries({ predicate: (q) => String(q.queryKey[0] ?? '').startsWith(key) })
      opts?.onSuccess?.(out, input)
    },
    onError: (e) => { if (!opts?.silent) toast(errorMessage(e), 'error') },
  })
}

/** A protected image (the gateway wants the bearer token, so a plain <img src> cannot load it) as an object URL. */
export function useAuthedImage(path: string | null) {
  const [state, setState] = useState<{ path: string | null; url?: string; error?: string }>({ path: null })
  useEffect(() => {
    if (!path) return
    let alive = true
    let obj: string | undefined
    api<Blob>(path, { raw: true })
      .then((b) => { if (!alive) return; obj = URL.createObjectURL(b); setState({ path, url: obj }) })
      .catch((e) => { if (alive) setState({ path, error: errorMessage(e) }) })
    return () => { alive = false; if (obj) URL.revokeObjectURL(obj) }
  }, [path])
  return state.path === path ? { url: state.url, error: state.error, loading: !state.url && !state.error && !!path } : { url: undefined, error: undefined, loading: !!path }
}

/** Distinct item categories (there is no categories endpoint; derived from the item master, cached 5 min). */
export function useCategories() {
  const q = useList<ItemFull>('/bhandar/v1/items', { limit: 500, status: 'all' }, { staleTime: 5 * 60_000 })
  return useMemo(() => [...new Set((q.data?.items ?? []).map((i) => i.category?.trim()).filter((c): c is string => !!c))].sort((a, b) => a.localeCompare(b)), [q.data])
}

/** Copy text to the clipboard with a toast; falls back to a prompt when the clipboard API is unavailable. */
export function useCopy() {
  const toast = useToast()
  return async (text: string, what = 'Copied') => {
    try {
      await navigator.clipboard.writeText(text)
      toast(what)
    } catch {
      window.prompt('Copy this value', text)
    }
  }
}
