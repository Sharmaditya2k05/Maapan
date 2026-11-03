'use client'

import { Suspense, useState } from 'react'
import Link from 'next/link'
import { ArrowRight, FileCheck2, FileText, Search, Settings, ShieldCheck, Truck } from 'lucide-react'
import { Badge, Card, PageHeader, QueryState, Skeleton, Tabs } from '@/components/ui'
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
import { Gstr2bView } from '@/components/ledger/gst/Gstr2bView'
import { EInvoiceEway } from '@/components/ledger/gst/EInvoiceEway'
import { TaxSettingsForm } from '@/components/ledger/gst/TaxSettingsForm'
import { GstinCheck, HsnFinder } from '@/components/ledger/gst/Tools'
import { RegistrationPicker, useRegistrations } from '@/components/compliance/registrations'

const TABS = ['overview', 'gstin', 'hsn', 'gstr1', 'gstr3b', 'gstr2b', 'einvoice', 'settings'] as const
type Tab = (typeof TABS)[number]

export default function GstPage() {
  return <Suspense fallback={<Skeleton />}><GstInner /></Suspense>
}

function GstInner() {
  const canDo = usePerm()
  const [tab, setTab] = useTabParam<Tab>(TABS, 'overview')
  const [period, setPeriod] = useState(defaultPeriod)
  // '' = the primary registration (kar's default when gstin= is empty).
  const [gstin, setGstin] = useState('')
  const canReturns = canDo(PERMS.reportRead) || canDo(PERMS.gstFile)
  const settings = useGet<TaxSettings>(canReturns ? '/kar/v1/settings' : null)
  const regs = useRegistrations()
  const selected = (gstin ? regs.fileable.find((r) => r.gstin === gstin) : undefined) ?? regs.primary
  const many = regs.fileable.length > 1

  if (!canReturns && !canDo(PERMS.partyManage) && !canDo(PERMS.invoiceRead)) return <><PageHeader title="GST" /><Card><NoPermission what="GST" /></Card></>

  const regBar = (withPeriod: boolean) => (
    <div className="no-print mb-4 flex flex-wrap items-center gap-2">
      {withPeriod && <><span className="text-xs font-semibold uppercase tracking-wide text-slate-500">Period</span><PeriodPicker value={period} onChange={setPeriod} /></>}
      <span className="text-xs font-semibold uppercase tracking-wide text-slate-500">GSTIN</span>
      <RegistrationPicker value={gstin} onChange={setGstin} />
      {selected && !selected.is_primary && <Badge tone="amber">filing for another registration</Badge>}
      {selected?.composition && <Badge tone="amber">composition</Badge>}
    </div>
  )
  const subtitle = selected
    ? <>{selected.gstin || 'Unregistered'} · {STATES[selected.state_code] ?? selected.state_code}{selected.city ? ` · ${selected.city}` : ''}{selected.composition ? ' · composition' : ''}{selected.einvoice_applicable ? ' · e-invoicing' : ''}{many ? ` · ${regs.fileable.length} registrations` : ''}</>
    : settings.data ? `${settings.data.gstin || 'Unregistered'} · ${STATES[settings.data.state_code] ?? settings.data.state_code}` : 'Returns, e-invoicing and tax tools'
  const g = gstin || undefined

  return (
    <>
      <PageHeader title="GST" subtitle={subtitle} />
      <div className="no-print mb-4">
        <Tabs value={tab} onChange={setTab} options={[
          { value: 'overview', label: 'Overview' }, { value: 'gstin', label: 'GSTIN check' }, { value: 'hsn', label: 'HSN finder' },
          ...(canReturns ? [{ value: 'gstr1' as Tab, label: 'GSTR-1' }, { value: 'gstr3b' as Tab, label: 'GSTR-3B' }, { value: 'gstr2b' as Tab, label: 'GSTR-2B & IMS' }] : []),
          ...(canDo(PERMS.invoiceRead) ? [{ value: 'einvoice' as Tab, label: 'e-Invoice & e-Way' }] : []),
          ...(canReturns ? [{ value: 'settings' as Tab, label: 'Settings' }] : []),
        ]} />
      </div>

      {tab === 'overview' && (
        <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_340px]">
          <Card title="This registration" actions={many ? <RegistrationPicker value={gstin} onChange={setGstin} /> : null}>
            {regs.isLoading ? <Skeleton rows={4} /> : selected ? (
              <dl className="divide-y divide-slate-100">
                <KV k="GSTIN" v={selected.gstin || 'Not registered'} mono />
                {(selected.legal_name || selected.trade_name) && <KV k="Name" v={selected.trade_name || selected.legal_name} />}
                <KV k="State" v={`${STATES[selected.state_code] ?? selected.state_code} (${selected.state_code})`} />
                <KV k="City / PIN" v={selected.city || selected.pin ? `${selected.city || '—'} ${selected.pin}` : <Badge tone="amber">missing — needed for e-invoices</Badge>} />
                <KV k="Scheme" v={<Badge tone={selected.composition ? 'amber' : 'green'}>{selected.composition ? 'Composition' : 'Regular'}</Badge>} />
                <KV k="e-Invoicing" v={<Badge tone={selected.einvoice_applicable ? 'green' : 'gray'}>{selected.einvoice_applicable ? 'applies' : 'off'}</Badge>} />
                <KV k="Stores" v={selected.store_count} />
                {canReturns && settings.data && <><KV k="e-Way bill threshold" v={formatINR(settings.data.eway_threshold_paise)} /><KV k="B2CL threshold" v={formatINR(settings.data.b2cl_threshold_paise)} /></>}
                <KV k="Updated" v={fmtDate(selected.updated_at, true)} />
              </dl>
            ) : !canReturns ? <NoPermission what="tax settings" /> : (
              <QueryState isLoading={settings.isLoading} error={settings.error ?? regs.error} data={settings.data} retry={() => settings.refetch()}>
                {(s) => (
                  <dl className="divide-y divide-slate-100">
                    <KV k="GSTIN" v={s.gstin || 'Not registered'} mono />
                    <KV k="State" v={`${STATES[s.state_code] ?? s.state_code} (${s.state_code})`} />
                    <KV k="e-Way bill threshold" v={formatINR(s.eway_threshold_paise)} />
                  </dl>
                )}
              </QueryState>
            )}
            {canDo(PERMS.settingsManage) && <Link href="/app/settings?tab=registrations" className="link mt-3 inline-block text-xs">Manage registrations</Link>}
          </Card>
          <div className="space-y-3">
            <Card title="Return period">
              <div className="flex flex-wrap items-center gap-2"><PeriodPicker value={period} onChange={setPeriod} /><span className="text-xs text-slate-500">Filing {monthLabel(period)}</span></div>
              <p className="mt-2 text-xs text-slate-500">GSTR-1 is due on the 11th and GSTR-3B on the 20th of the following month for monthly filers. Act on GSTR-2B documents in IMS before filing GSTR-3B.</p>
            </Card>
            {([
              ['gstr1', 'GSTR-1 — outward supplies', FileText, canReturns], ['gstr3b', 'GSTR-3B — summary & tax payable', FileText, canReturns],
              ['gstr2b', 'GSTR-2B match & IMS actions', FileCheck2, canReturns],
              ['einvoice', 'e-Invoices (IRN) & e-Way bills', Truck, canDo(PERMS.invoiceRead)], ['gstin', 'Verify a GSTIN', ShieldCheck, canDo(PERMS.partyManage)],
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
      {tab === 'gstr1' && canReturns && <>{regBar(true)}<Gstr1View period={period} gstin={g} /></>}
      {tab === 'gstr3b' && canReturns && <>{regBar(true)}<Gstr3bView period={period} gstin={g} /></>}
      {tab === 'gstr2b' && canReturns && <>{regBar(true)}<Gstr2bView period={period} gstin={g} /></>}
      {tab === 'einvoice' && canDo(PERMS.invoiceRead) && <>{regBar(false)}<EInvoiceEway gstin={selected?.gstin || g} /></>}
      {tab === 'settings' && canReturns && <TaxSettingsForm />}
    </>
  )
}
