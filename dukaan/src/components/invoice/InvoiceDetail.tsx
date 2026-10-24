'use client'

/**
 * Everything on a stored document — lines, tax summary, totals, tenders,
 * payment position, parties, notes — with document-type wording. The page
 * around it supplies the actions.
 */
import Link from 'next/link'
import type { ReactNode } from 'react'
import { ArrowUpRight } from 'lucide-react'
import { Badge, Card, Money, statusTone } from '@/components/ui'
import { fmtDate, fmtQty } from '@/lib/hooks'
import { formatRate } from '@/lib/money'
import type { Party } from '@/lib/types'
import type { InvoiceFull } from '@/lib/types.pos'
import { CATEGORY_LABEL, DOC_SHORT, documentTitle, isNote, isPurchaseSide, stateName, summarizeByRate, TENDER_LABEL } from '@/lib/pos/invoice'

export function InvoiceBadges({ inv }: { inv: InvoiceFull }) {
  return (
    <>
      <Badge tone={statusTone(inv.status)}>{inv.status}</Badge>
      {inv.status === 'posted' && !isNote(inv.doc_type) && <Badge tone={statusTone(inv.payment_status)}>{inv.payment_status}</Badge>}
      {inv.inter_state && <Badge tone="blue">IGST</Badge>}
      {inv.reverse_charge && <Badge tone="amber">Reverse charge</Badge>}
    </>
  )
}

export function InvoiceDetail({ invoice: inv, party, refInvoice, children }: { invoice: InvoiceFull; party?: Party; refInvoice?: InvoiceFull; children?: ReactNode }) {
  const purchase = isPurchaseSide(inv.doc_type)
  const note = isNote(inv.doc_type)
  const inter = inv.inter_state
  const rows = summarizeByRate(inv.lines)
  const lines = inv.lines ?? []
  const hasCess = inv.cess_paise > 0
  const partyLabel = purchase ? 'Supplier' : 'Customer'
  const refHref = refInvoice ? (refInvoice.doc_type === 'purchase' ? `/app/purchases/${refInvoice.id}` : `/app/sales/${refInvoice.id}`) : undefined

  return (
    <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_320px] sm:gap-5">
      <div className="min-w-0 space-y-4 sm:space-y-5">
        <Card padded={false} title={`${lines.length} line${lines.length === 1 ? '' : 's'}`} actions={<span className="text-xs text-slate-500">Qty {fmtQty(lines.reduce((a, l) => a + l.qty, 0))}</span>}>
          {/* mobile */}
          <ul className="divide-y divide-slate-100 md:hidden">
            {lines.map((l) => (
              <li key={l.line_no} className="px-4 py-3">
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <div className="text-sm font-semibold">{l.name}</div>
                    <div className="text-xs text-slate-500">{[l.hsn && `HSN ${l.hsn}`, l.batch_no && `Batch ${l.batch_no}`, l.tax_category && l.tax_category !== 'taxable' ? CATEGORY_LABEL[l.tax_category] : `GST ${formatRate(l.tax_rate_bp)}`].filter(Boolean).join(' · ')}</div>
                    <div className="mt-0.5 text-xs text-slate-600">{fmtQty(l.qty)} {l.unit} × <Money paise={l.unit_price_paise} />{l.discount_paise ? <> − <Money paise={l.discount_paise} /></> : null}</div>
                  </div>
                  <Money paise={l.total_paise} className="shrink-0 text-sm font-bold" />
                </div>
              </li>
            ))}
          </ul>
          {/* desktop */}
          <div className="hidden overflow-x-auto md:block">
            <table className="w-full min-w-[760px]">
              <thead className="bg-slate-50/60">
                <tr>
                  <th className="th w-8">#</th><th className="th">Item</th><th className="th">HSN</th><th className="th text-right">Qty</th><th className="th text-right">Rate</th>
                  <th className="th text-right">Disc.</th><th className="th text-right">Taxable</th>
                  {inter ? <th className="th text-right">IGST</th> : <><th className="th text-right">CGST</th><th className="th text-right">SGST</th></>}
                  {hasCess && <th className="th text-right">Cess</th>}
                  <th className="th text-right">Total</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100">
                {lines.map((l) => (
                  <tr key={l.line_no}>
                    <td className="td text-xs text-slate-400">{l.line_no}</td>
                    <td className="td">
                      <div className="font-semibold">{l.name}</div>
                      <div className="text-xs text-slate-500">{[l.batch_no && `Batch ${l.batch_no}`, l.tax_category && l.tax_category !== 'taxable' ? CATEGORY_LABEL[l.tax_category] : `GST ${formatRate(l.tax_rate_bp)}`, l.price_includes_tax ? 'incl.' : ''].filter(Boolean).join(' · ')}</div>
                    </td>
                    <td className="td font-mono text-xs text-slate-500">{l.hsn || '—'}</td>
                    <td className="td num text-right">{fmtQty(l.qty)} <span className="text-xs text-slate-400">{l.unit}</span></td>
                    <td className="td num text-right"><Money paise={l.unit_price_paise} /></td>
                    <td className="td num text-right text-slate-500">{l.discount_paise ? <Money paise={l.discount_paise} /> : '—'}</td>
                    <td className="td num text-right"><Money paise={l.taxable_paise} /></td>
                    {inter ? <td className="td num text-right"><Money paise={l.igst_paise} /></td> : <><td className="td num text-right"><Money paise={l.cgst_paise} /></td><td className="td num text-right"><Money paise={l.sgst_paise} /></td></>}
                    {hasCess && <td className="td num text-right"><Money paise={l.cess_paise} /></td>}
                    <td className="td num text-right font-semibold"><Money paise={l.total_paise} /></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>

        <div className="grid gap-4 md:grid-cols-2 sm:gap-5">
          <Card title="Tax summary" padded={false}>
            <table className="w-full">
              <thead className="bg-slate-50/60"><tr><th className="th">Rate</th><th className="th text-right">Taxable</th>{inter ? <th className="th text-right">IGST</th> : <><th className="th text-right">CGST</th><th className="th text-right">SGST</th></>}{hasCess && <th className="th text-right">Cess</th>}</tr></thead>
              <tbody className="divide-y divide-slate-100">
                {rows.map((r) => (
                  <tr key={`${r.tax_rate_bp}:${r.tax_category}`}>
                    <td className="td">{r.tax_category !== 'taxable' ? CATEGORY_LABEL[r.tax_category] : formatRate(r.tax_rate_bp)}</td>
                    <td className="td num text-right"><Money paise={r.taxable_paise} /></td>
                    {inter ? <td className="td num text-right"><Money paise={r.igst_paise} /></td> : <><td className="td num text-right"><Money paise={r.cgst_paise} /></td><td className="td num text-right"><Money paise={r.sgst_paise} /></td></>}
                    {hasCess && <td className="td num text-right"><Money paise={r.cess_paise} /></td>}
                  </tr>
                ))}
                {rows.length === 0 && <tr><td className="td text-slate-500" colSpan={5}>Lines were not included in this response.</td></tr>}
              </tbody>
            </table>
          </Card>
          <Card title={partyLabel}>
            <div className="text-sm font-bold">{inv.party_name}</div>
            <div className="mt-1 space-y-0.5 text-xs text-slate-600">
              {inv.party_phone && <div>{inv.party_phone}</div>}
              {party?.address && <div className="whitespace-pre-line">{party.address}</div>}
              {inv.party_gstin ? <div className="font-mono font-semibold text-ink">GSTIN {inv.party_gstin}</div> : <div>Unregistered</div>}
              {(inv.party_state || party?.state_code) && <div>State: {stateName(inv.party_state || party?.state_code)}</div>}
              <div>Place of supply: {stateName(inv.place_of_supply)}</div>
            </div>
            {party && (
              <div className="mt-3 flex items-center justify-between rounded-xl bg-slate-50 px-3 py-2 text-xs">
                <span className="text-slate-500">Ledger balance</span>
                <Money paise={party.balance_paise} signed className="font-semibold" />
              </div>
            )}
            {inv.party_id && <Link href={`/app/parties/${inv.party_id}`} className="link mt-3 inline-flex items-center gap-1 text-xs">Open ledger <ArrowUpRight size={12} /></Link>}
          </Card>
        </div>

        {(inv.notes || inv.cancel_reason) && (
          <Card title="Notes">
            {inv.notes && <p className="whitespace-pre-line text-sm text-slate-700">{inv.notes}</p>}
            {inv.cancel_reason && <p className="mt-2 rounded-xl bg-rose-50 px-3 py-2 text-sm text-rose-700"><span className="font-semibold">Cancelled{inv.cancelled_at ? ` on ${fmtDate(inv.cancelled_at, true)}` : ''}:</span> {inv.cancel_reason}</p>}
          </Card>
        )}
        {children}
      </div>

      <aside className="space-y-4 xl:sticky xl:top-20 xl:self-start sm:space-y-5">
        <Card>
          <dl className="space-y-1.5 text-sm">
            <Row l="Taxable value" v={inv.taxable_paise} />
            {inv.discount_paise > 0 && <Row l="Discounts (incl.)" v={inv.discount_paise} muted />}
            {!!inv.invoice_discount_paise && <Row l="of which invoice discount" v={inv.invoice_discount_paise} muted />}
            {inter ? <Row l="IGST" v={inv.igst_paise} /> : <><Row l="CGST" v={inv.cgst_paise} /><Row l="SGST" v={inv.sgst_paise} /></>}
            {hasCess && <Row l="Cess" v={inv.cess_paise} />}
            {!!inv.tcs_paise && <Row l="TCS" v={inv.tcs_paise} />}
            {inv.round_off_paise !== 0 && <Row l="Round off" v={inv.round_off_paise} muted signed />}
          </dl>
          <div className="mt-3 rounded-2xl bg-gradient-to-br from-brand-700 to-brand-900 p-4 text-white">
            <div className="text-xs text-brand-200">{documentTitle(inv)} total</div>
            <Money paise={inv.grand_total_paise} className="text-3xl font-extrabold" />
            <div className="mt-1 text-[11px] leading-snug text-brand-200">{inv.amount_in_words}</div>
          </div>
        </Card>

        {!note && (
          <Card title={purchase ? 'Payment to supplier' : 'Payment'}>
            <div className="flex items-end justify-between">
              <div>
                <div className="text-xs text-slate-500">Balance {purchase ? 'payable' : 'due'}</div>
                <Money paise={inv.balance_paise} className={`text-2xl font-extrabold ${inv.balance_paise > 0 ? 'text-amber-600' : 'text-emerald-600'}`} />
              </div>
              <Badge tone={statusTone(inv.payment_status)}>{inv.payment_status}</Badge>
            </div>
            <div className="mt-3 h-2 overflow-hidden rounded-full bg-slate-100"><div className="h-full rounded-full bg-emerald-500" style={{ width: `${Math.min(100, ((inv.paid_paise + (inv.credited_paise ?? 0)) / (inv.grand_total_paise || 1)) * 100)}%` }} /></div>
            <dl className="mt-3 space-y-1.5 text-sm">
              <Row l={purchase ? 'Paid' : 'Received'} v={inv.paid_paise} />
              {!!inv.change_paise && <Row l="Change returned" v={inv.change_paise} muted />}
              {!!inv.credited_paise && <Row l={purchase ? 'Debit notes' : 'Credit notes'} v={inv.credited_paise} muted />}
              {inv.due_date && <div className="flex justify-between text-slate-500"><dt>Due date</dt><dd>{fmtDate(inv.due_date)}</dd></div>}
            </dl>
            {(inv.tenders?.length ?? 0) > 0 && (
              <ul className="mt-3 space-y-1.5">
                {inv.tenders!.map((t, i) => (
                  <li key={i} className="flex items-center justify-between rounded-xl bg-slate-50 px-3 py-2 text-sm">
                    <div><div className="font-semibold">{TENDER_LABEL[t.mode]}</div>{(t.reference || t.payment_request_id) && <div className="text-xs text-slate-500">{t.reference ?? t.payment_request_id}</div>}</div>
                    <Money paise={t.amount_paise} className="font-semibold" />
                  </li>
                ))}
              </ul>
            )}
            {inv.payment_status !== 'paid' && inv.status === 'posted' && (inv.tenders?.length ?? 0) === 0 && <p className="mt-2 text-xs text-slate-500">No counter tenders; receipts recorded in Payments update this balance.</p>}
          </Card>
        )}

        <Card title="Document">
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
            <dt className="text-slate-500">Type</dt><dd className="font-semibold">{DOC_SHORT[inv.doc_type]}</dd>
            <dt className="text-slate-500">Number</dt><dd className="font-mono font-semibold">{inv.number}</dd>
            <dt className="text-slate-500">Date</dt><dd>{fmtDate(inv.invoice_date)}</dd>
            <dt className="text-slate-500">Created</dt><dd>{fmtDate(inv.created_at, true)}</dd>
            {inv.supplier_invoice_no && <><dt className="text-slate-500">Supplier bill</dt><dd>{inv.supplier_invoice_no}{inv.supplier_invoice_date ? ` · ${fmtDate(inv.supplier_invoice_date)}` : ''}</dd></>}
            {inv.store_gstin && <><dt className="text-slate-500">Our GSTIN</dt><dd className="font-mono">{inv.store_gstin}</dd></>}
            <dt className="text-slate-500">Store state</dt><dd>{stateName(inv.store_state)}</dd>
            {inv.supply_category && <><dt className="text-slate-500">Supply</dt><dd>{inv.supply_category.replace(/_/g, ' ')}</dd></>}
            {inv.irn && <><dt className="text-slate-500">IRN</dt><dd className="break-all font-mono">{inv.irn}</dd></>}
            {refInvoice && <><dt className="text-slate-500">Against</dt><dd>{refHref ? <Link href={refHref} className="link font-mono">{refInvoice.number}</Link> : refInvoice.number}</dd></>}
            {inv.client_uuid && <><dt className="text-slate-500">Client ref</dt><dd className="break-all font-mono text-[10px] text-slate-400">{inv.client_uuid}</dd></>}
          </dl>
        </Card>
      </aside>
    </div>
  )
}

function Row({ l, v, muted, signed }: { l: string; v: number; muted?: boolean; signed?: boolean }) {
  return <div className={`flex justify-between ${muted ? 'text-slate-400' : 'text-slate-600'}`}><dt>{l}</dt><dd><Money paise={v} signed={signed} className="font-medium text-ink" /></dd></div>
}
