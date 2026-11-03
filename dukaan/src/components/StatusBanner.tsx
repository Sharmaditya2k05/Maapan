'use client'

/**
 * Small banner under the top bar when a service the current screen needs is down or still starting,
 * from dwaar's GET /api/_status (public, cached 10 s by the gateway). Plain fetch: no auth, no wake retries.
 */
import { usePathname } from 'next/navigation'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, Loader2, RefreshCw } from 'lucide-react'
import { API_URL } from '@/lib/api'
import type { GatewayStatus } from '@/lib/types.phase4'
import { servicesForPath, statusIssues, statusMessage } from '@/lib/ledger/phase4'

async function fetchStatus(): Promise<GatewayStatus | null> {
  try {
    const res = await fetch(`${API_URL}/_status`, { cache: 'no-store' })
    if (!res.ok || !(res.headers.get('Content-Type') || '').includes('json')) return null
    return (await res.json()) as GatewayStatus
  } catch {
    return null // the gateway itself is unreachable: the offline pill and request errors already say so
  }
}

export function StatusBanner() {
  const pathname = usePathname()
  const q = useQuery({
    queryKey: ['/_status'],
    queryFn: fetchStatus,
    staleTime: 10_000,
    retry: false,
    refetchInterval: (query) => (query.state.data?.status === 'degraded' ? 10_000 : 60_000),
  })
  const issues = statusIssues(q.data?.services, servicesForPath(pathname))
  if (issues.length === 0) return null
  const starting = issues.every((i) => i.state === 'starting')
  return (
    <div role="status" className={`no-print flex items-center gap-2 border-b px-3 py-2 text-xs sm:px-5 ${starting ? 'border-amber-200 bg-amber-50 text-amber-800' : 'border-rose-200 bg-rose-50 text-rose-700'}`}>
      {starting ? <Loader2 size={14} className="shrink-0 animate-spin" /> : <AlertTriangle size={14} className="shrink-0" />}
      <span className="min-w-0 flex-1">{statusMessage(issues)}</span>
      <button type="button" onClick={() => q.refetch()} className="inline-flex shrink-0 items-center gap-1 font-semibold underline cursor-pointer" aria-label="Check service status again">
        <RefreshCw size={12} className={q.isFetching ? 'animate-spin' : ''} /> Check again
      </button>
    </div>
  )
}
