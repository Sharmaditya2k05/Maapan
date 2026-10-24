'use client'

/** GET /buddhi/v1/insights/anomalies → findings with severity, resolve via POST …/{id}/resolve (204). */
import { useMemo, useState } from 'react'
import { AlertOctagon, AlertTriangle, CheckCircle2, Info, ShieldCheck } from 'lucide-react'
import { Badge, Button, Card, Empty, QueryState, Select, Table, Toggle, useToast, type Tone } from '@/components/ui'
import { fmtDate, useGet, useSave, useStore } from '@/lib/hooks'
import type { Anomaly, AnomalyReport, AnomalySeverity } from '@/lib/types.more'
import { StoreScope } from './common'

const SEV: Record<AnomalySeverity, { tone: Tone; icon: typeof Info; label: string }> = {
  critical: { tone: 'red', icon: AlertOctagon, label: 'Critical' },
  warning: { tone: 'amber', icon: AlertTriangle, label: 'Warning' },
  info: { tone: 'blue', icon: Info, label: 'Info' },
}
const kindLabel = (k: string) => k.replace(/_/g, ' ')

export function InsightsTab() {
  const { storeId } = useStore()
  const [store, setStore] = useState(storeId ?? '')
  const [days, setDays] = useState(30)
  const [kind, setKind] = useState('')
  const [resolved, setResolved] = useState(false)
  const q = useGet<AnomalyReport>('/buddhi/v1/insights/anomalies', { days, kind, store_id: store || undefined, include_resolved: resolved })
  const kinds = useMemo(() => Array.from(new Set((q.data?.anomalies ?? []).map((a) => a.kind))).sort(), [q.data])

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <StoreScope value={store || storeId || ''} onChange={setStore} allowAll />
        <Select className="w-auto" value={days} onChange={(e) => setDays(Number(e.target.value))} aria-label="Window">
          <option value={7}>Last 7 days</option><option value={30}>Last 30 days</option><option value={90}>Last 90 days</option><option value={180}>Last 180 days</option>
        </Select>
        {(kinds.length > 1 || kind) && (
          <Select className="w-auto" value={kind} onChange={(e) => setKind(e.target.value)} aria-label="Kind">
            <option value="">All kinds</option>{kinds.map((k) => <option key={k} value={k}>{kindLabel(k)}</option>)}{kind && !kinds.includes(kind) && <option value={kind}>{kindLabel(kind)}</option>}
          </Select>
        )}
        <Toggle checked={resolved} onChange={setResolved} label="Show resolved" />
      </div>
      <QueryState isLoading={q.isLoading} error={q.error} data={q.data} retry={() => q.refetch()}>
        {(d) => (
          <>
            <Card title={`Findings since ${fmtDate(d.since)}`} padded={false}>
              {d.anomalies.length === 0 ? <Empty icon={ShieldCheck} title="Nothing unusual" text="Discount outliers, GSTIN problems and rate mismatches show up here as they are detected." /> : (
                <ul className="divide-y divide-slate-100">{d.anomalies.map((a) => <AnomalyRow key={a.id} a={a} />)}</ul>
              )}
            </Card>
            {d.high_cancellation_cashiers.length > 0 && (
              <Card title="Cashiers with unusual cancellation rates" padded={false}>
                <Table>
                  <thead><tr><th className="th">Cashier</th><th className="th text-right">Bills</th><th className="th text-right">Cancelled</th><th className="th text-right">Rate</th><th className="th text-right">Business avg</th></tr></thead>
                  <tbody>{d.high_cancellation_cashiers.map((c) => (
                    <tr key={c.cashier_id} className="border-t border-slate-100">
                      <td className="td num text-xs">{c.cashier_id}</td><td className="td num text-right">{c.bills}</td><td className="td num text-right">{c.cancelled}</td>
                      <td className="td num text-right font-semibold text-rose-700">{c.rate_pct.toFixed(1)}%</td><td className="td num text-right text-slate-500">{c.tenant_avg_pct.toFixed(1)}%</td>
                    </tr>
                  ))}</tbody>
                </Table>
                <p className="px-4 py-3 text-xs text-slate-500">Flagged when a cashier has 3+ cancellations and a rate above 5% and three times the business average. Cashier ids map to users in Settings → Users.</p>
              </Card>
            )}
          </>
        )}
      </QueryState>
    </div>
  )
}

function AnomalyRow({ a }: { a: Anomaly }) {
  const toast = useToast()
  const { stores } = useStore()
  const sev = SEV[a.severity] ?? SEV.info
  const Icon = sev.icon
  const resolve = useSave<void>(`/buddhi/v1/insights/anomalies/${a.id}/resolve`, { invalidate: ['/buddhi/v1/insights/anomalies'], onSuccess: () => toast('Marked as handled') })
  const details = a.details && typeof a.details === 'object' && !Array.isArray(a.details) ? Object.entries(a.details as Record<string, unknown>) : []
  const store = a.store_id ? stores.find((s) => s.id === a.store_id)?.name : undefined
  return (
    <li className={`flex items-start gap-3 px-4 py-3 ${a.resolved ? 'opacity-60' : ''}`}>
      <Icon size={18} className={`mt-0.5 shrink-0 ${sev.tone === 'red' ? 'text-rose-600' : sev.tone === 'amber' ? 'text-amber-500' : 'text-sky-600'}`} aria-hidden />
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <Badge tone={sev.tone}>{sev.label}</Badge>
          <span className="text-xs text-slate-500">{kindLabel(a.kind)}</span>
          {store && <span className="text-xs text-slate-500">· {store}</span>}
          {a.resolved && <Badge tone="green"><CheckCircle2 size={12} /> handled</Badge>}
        </div>
        <div className="mt-1 text-sm font-semibold">{a.subject}</div>
        <div className="text-sm text-slate-600">{a.message}</div>
        {details.length > 0 && (
          <dl className="mt-1 flex flex-wrap gap-x-4 gap-y-0.5 text-xs text-slate-500">
            {details.slice(0, 8).map(([k, v]) => <div key={k}><dt className="inline text-slate-400">{kindLabel(k)}: </dt><dd className="num inline">{typeof v === 'object' ? JSON.stringify(v) : String(v)}</dd></div>)}
          </dl>
        )}
        <div className="mt-1 text-[11px] text-slate-400">{fmtDate(a.detected_at, true)}</div>
      </div>
      {!a.resolved && <Button size="sm" variant="secondary" loading={resolve.isPending} onClick={() => resolve.mutate(undefined)}>Resolve</Button>}
    </li>
  )
}
