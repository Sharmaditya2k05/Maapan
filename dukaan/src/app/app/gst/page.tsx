'use client'

import { Suspense, useState } from 'react'
import { ArrowRight, FileText, Search, Settings, ShieldCheck, Truck } from 'lucide-react'
import { Badge, Card, Input, PageHeader, QueryState, Skeleton, Tabs } from '@/components/ui'
import { fmtDate, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { STATES } from '@/lib/gst'
import type { TaxSettings } from '@/lib/types.ledger'
import { monthLabel } from '@/lib/ledger/util'
import { formatINR } from '@/lib/money'
import { KV, NoPermission, useTabParam } from '@/components/ledger/bits'
import { defaultPeriod, PeriodPicker } from '@/components/ledger/gst/shared'
import { Gstr1View } from '@/components/ledger/gst/Gstr1View'
import { Gstr3bView } from '@/components/ledger/gst/Gstr3bView'
import { EInvoiceEway } from '@/components/ledger/gst/EInvoiceEway'
import { TaxSettingsForm } from '@/components/ledger/gst/TaxSettingsForm'
import { GstinCheck, HsnFinder } from '@/components/ledger/gst/Tools'

const TABS = ['overview', 'gstin', 'hsn', 'gstr1', 'gstr3b', 'einvoice', 'settings'] as const
type Tab = (typeof TABS)[number]

export default function GstPage() {
  return <Suspense fallback={<Skeleton />}><GstInner /></Suspense>
}

function GstInner() {
  const canDo = usePerm()
  const [tab, setTab] = useTabParam<Tab>(TABS, 'overview')
  const [period, setPeriod] = useState(defaultPeriod)
  const [gstin, setGstin] = useState('')
  const canReturns = canDo(PERMS.reportRead) || canDo(PERMS.gstFile)
  const settings = useGet<TaxSettings>(canReturns ? '/kar/v1/settings' : null)

  if (!canReturns && !canDo(PERMS.partyManage)) return <><PageHeader title="GST" /><Card><NoPermission what="GST" /></Card></>

  const returnsBar = (
    <div className="no-print mb-4 flex flex-wrap items-center gap-2">
      <span className="text-xs font-semibold uppercase tracking-wide text-slate-500">Period</span>
      <PeriodPicker value={period} onChange={setPeriod} />
      <Input value={gstin} onChange={(e) => setGstin(e.target.value.toUpperCase())} className="w-48 input-sm font-mono uppercase" placeholder={settings.data?.gstin || 'GSTIN (default)'} aria-label="GSTIN for a second registration" maxLength={15} />
      {gstin && gstin !== settings.data?.gstin && <Badge tone="amber">filing for another registration</Badge>}
    </div>
  )

  return (
    <>
      <PageHeader title="GST" subtitle={settings.data ? `${settings.data.gstin || 'Unregistered'} · ${STATES[settings.data.state_code] ?? settings.data.state_code}${settings.data.composition ? ' · composition' : ''}` : 'Returns, e-invoicing and tax tools'} />
      <div className="no-print mb-4">
        <Tabs value={tab} onChange={setTab} options={[
          { value: 'overview', label: 'Overview' }, { value: 'gstin', label: 'GSTIN check' }, { value: 'hsn', label: 'HSN finder' },
          ...(canReturns ? [{ value: 'gstr1' as Tab, label: 'GSTR-1' }, { value: 'gstr3b' as Tab, label: 'GSTR-3B' }] : []),
          ...(canDo(PERMS.invoiceRead) ? [{ value: 'einvoice' as Tab, label: 'e-Invoice & e-Way' }] : []),
          ...(canReturns ? [{ value: 'settings' as Tab, label: 'Settings' }] : []),
        ]} />
      </div>

      {tab === 'overview' && (
        <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_340px]">
          <Card title="This registration">
            {!canReturns ? <NoPermission what="tax settings" /> : (
              <QueryState isLoading={settings.isLoading} error={settings.error} data={settings.data} retry={() => settings.refetch()}>
                {(s) => (
                  <dl className="divide-y divide-slate-100">
                    <KV k="GSTIN" v={s.gstin || 'Not registered'} mono />
                    <KV k="State" v={`${STATES[s.state_code] ?? s.state_code} (${s.state_code})`} />
                    <KV k="Scheme" v={<Badge tone={s.composition ? 'amber' : 'green'}>{s.composition ? 'Composition' : 'Regular'}</Badge>} />
                    <KV k="e-Invoicing" v={<Badge tone={s.einvoice_enabled ? 'green' : 'gray'}>{s.einvoice_enabled ? 'enabled' : 'off'}</Badge>} />
                    <KV k="e-Way bill threshold" v={formatINR(s.eway_threshold_paise)} />
                    <KV k="B2CL threshold" v={formatINR(s.b2cl_threshold_paise)} />
                    <KV k="Updated" v={fmtDate(s.updated_at, true)} />
                  </dl>
                )}
              </QueryState>
            )}
          </Card>
          <div className="space-y-3">
            <Card title="Return period">
              <div className="flex flex-wrap items-center gap-2"><PeriodPicker value={period} onChange={setPeriod} /><span className="text-xs text-slate-500">Filing {monthLabel(period)}</span></div>
              <p className="mt-2 text-xs text-slate-500">GSTR-1 is due on the 11th and GSTR-3B on the 20th of the following month for monthly filers.</p>
            </Card>
            {([
              ['gstr1', 'GSTR-1 — outward supplies', FileText, canReturns], ['gstr3b', 'GSTR-3B — summary & tax payable', FileText, canReturns],
              ['einvoice', 'e-Invoice & e-Way bill JSON', Truck, canDo(PERMS.invoiceRead)], ['gstin', 'Verify a GSTIN', ShieldCheck, canDo(PERMS.partyManage)],
              ['hsn', 'Find HSN / SAC codes and rates', Search, true], ['settings', 'Tax settings', Settings, canReturns],
            ] as const).filter((x) => x[3]).map(([t, label, Icon]) => (
              <button key={t} type="button" onClick={() => setTab(t)} className="card flex w-full items-center gap-3 px-4 py-3 text-left text-sm font-semibold hover:border-brand-300 cursor-pointer">
                <Icon size={16} className="text-brand-700" /><span className="flex-1">{label}</span><ArrowRight size={14} className="text-slate-300" />
              </button>
            ))}
          </div>
        </div>
      )}
      {tab === 'gstin' && <GstinCheck />}
      {tab === 'hsn' && <HsnFinder />}
      {tab === 'gstr1' && canReturns && <>{returnsBar}<Gstr1View period={period} gstin={gstin.length === 15 ? gstin : undefined} /></>}
      {tab === 'gstr3b' && canReturns && <>{returnsBar}<Gstr3bView period={period} gstin={gstin.length === 15 ? gstin : undefined} /></>}
      {tab === 'einvoice' && canDo(PERMS.invoiceRead) && <EInvoiceEway />}
      {tab === 'settings' && canReturns && <TaxSettingsForm />}
    </>
  )
}
