'use client'

/** GSTIN check and HSN finder tabs (kar POST /v1/gstin/verify, GET /v1/hsn, GET /v1/hsn/{code}). */
import { useState } from 'react'
import { Search, ShieldCheck } from 'lucide-react'
import { Badge, Button, Card, Empty, Input, QueryState, SearchInput, Table } from '@/components/ui'
import { post } from '@/lib/api'
import { useDebounced, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { validateGSTIN } from '@/lib/gst'
import { formatRate } from '@/lib/money'
import type { GstinResult, HSN } from '@/lib/types.ledger'
import { GstinCard } from '../GstinCard'
import { KV, NoPermission } from '../bits'
import { useAction } from '../useAction'

export function GstinCheck() {
  const canDo = usePerm()
  const [gstin, setGstin] = useState('')
  const [result, setResult] = useState<GstinResult | null>(null)
  const verify = useAction<GstinResult, string>((g) => post<GstinResult>('/kar/v1/gstin/verify', { gstin: g }), { onSuccess: setResult })
  const g = gstin.trim().toUpperCase()
  const err = g ? validateGSTIN(g) : null
  if (!canDo(PERMS.partyManage)) return <Card><NoPermission what="GSTIN verification" /></Card>
  return (
    <Card title="Check a GSTIN">
      <form className="flex flex-col gap-2 sm:flex-row" onSubmit={(e) => { e.preventDefault(); if (g && !err) verify.mutate(g) }}>
        <Input value={gstin} onChange={(e) => { setGstin(e.target.value.toUpperCase()); setResult(null) }} className="font-mono uppercase sm:max-w-xs" placeholder="27AAPFU0939F1ZV" maxLength={15} autoFocus aria-label="GSTIN" />
        <Button type="submit" icon={ShieldCheck} loading={verify.isPending} disabled={!g || !!err}>Verify</Button>
      </form>
      {g && err && <p className="mt-2 text-xs text-rose-600">{err}</p>}
      {g && !err && !result && <p className="mt-2 text-xs text-emerald-700">Format and checksum look right. Verify to check with the portal.</p>}
      {result && <div className="mt-4"><GstinCard result={result} /></div>}
      <p className="mt-4 text-xs text-slate-500">Live portal lookups need a GSP contract; without one kar validates the checksum only and says so.</p>
    </Card>
  )
}

export function HsnFinder() {
  const [q, setQ] = useState('')
  const dq = useDebounced(q)
  const [code, setCode] = useState<string | null>(null)
  const list = useGet<{ items: HSN[] }>('/kar/v1/hsn', { q: dq, limit: 50 })
  const one = useGet<HSN>(code ? `/kar/v1/hsn/${code}` : null)
  return (
    <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_320px]">
      <Card padded={false}>
        <div className="border-b border-slate-100 p-3 sm:p-4"><SearchInput value={q} onChange={setQ} placeholder="HSN/SAC code or description (biscuit, 1905…)" autoFocus className="md:w-96" /></div>
        <QueryState isLoading={list.isLoading} error={list.error} data={list.data} retry={() => list.refetch()}>
          {(d) => d.items.length === 0 ? <Empty icon={Search} title="No HSN/SAC matches" text="The master holds a retail starter set; verify rates against the latest CBIC notification." /> : (
            <Table>
              <thead className="bg-slate-50/60"><tr><th className="th">Code</th><th className="th">Description</th><th className="th">Kind</th><th className="th text-right">GST</th><th className="th text-right">Cess</th></tr></thead>
              <tbody className="divide-y divide-slate-100">
                {d.items.map((h) => (
                  <tr key={h.code} onClick={() => setCode(h.code)} className={`cursor-pointer hover:bg-brand-50/40 ${code === h.code ? 'bg-brand-50/60' : ''}`}>
                    <td className="td font-mono text-xs font-semibold">{h.code}</td><td className="td">{h.description}</td><td className="td"><Badge tone={h.kind === 'service' ? 'blue' : 'gray'}>{h.kind}</Badge></td>
                    <td className="td num text-right">{formatRate(h.default_rate_bp)}</td><td className="td num text-right text-slate-500">{h.cess_rate_bp ? formatRate(h.cess_rate_bp) : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </QueryState>
      </Card>
      <Card title={code ? `HSN ${code}` : 'Details'}>
        {!code ? <p className="text-sm text-slate-500">Pick a code to see its details.</p> : (
          <QueryState isLoading={one.isLoading} error={one.error} data={one.data} retry={() => one.refetch()}>
            {(h) => <dl className="divide-y divide-slate-100"><KV k="Code" v={h.code} mono /><KV k="Description" v={h.description} /><KV k="Kind" v={h.kind} /><KV k="Default GST rate" v={formatRate(h.default_rate_bp)} /><KV k="Cess" v={h.cess_rate_bp ? formatRate(h.cess_rate_bp) : 'None'} /></dl>}
          </QueryState>
        )}
      </Card>
    </div>
  )
}
