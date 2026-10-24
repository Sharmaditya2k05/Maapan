'use client'

/**
 * GST-compliant A4 tax invoice / bill of supply / credit / debit note.
 * Render inside `.print-root.print-a4` (see globals.css). Pure presentation:
 * everything comes from the stored invoice, the issuing store and the tenant name.
 */
import { formatINR, formatRate } from '@/lib/money'
import { fmtDate, fmtQty } from '@/lib/hooks'
import type { Party, Store } from '@/lib/types'
import type { InvoiceFull } from '@/lib/types.pos'
import { CATEGORY_LABEL, documentTitle, isNote, isPurchaseSide, stateName, summarizeByRate, TENDER_LABEL } from '@/lib/pos/invoice'

export function InvoiceA4({ invoice: inv, store, businessName, party, copy = 'ORIGINAL FOR RECIPIENT', refNumber }: {
  invoice: InvoiceFull; store?: Store; businessName?: string; party?: Party; copy?: string; refNumber?: string
}) {
  const title = documentTitle(inv)
  const inter = inv.inter_state
  const rows = summarizeByRate(inv.lines)
  const purchase = isPurchaseSide(inv.doc_type)
  const seller = businessName || store?.name || ''
  const tax = inv.cgst_paise + inv.sgst_paise + inv.igst_paise + inv.cess_paise
  const hasCess = inv.cess_paise > 0
  const hasDisc = (inv.lines ?? []).some((l) => (l.discount_paise ?? 0) > 0)
  const cols = 8 + (inter ? 2 : 4) + (hasCess ? 1 : 0) + (hasDisc ? 1 : 0) - 1
  const buyerLabel = purchase ? 'Supplier (bill from)' : 'Bill to (recipient)'

  return (
    <article className="mx-auto w-full max-w-[186mm] bg-white text-[10.5pt] leading-snug text-black">
      {/* header */}
      <header className="flex items-start justify-between gap-4 border-b-2 border-black pb-3">
        <div className="min-w-0">
          <div className="text-xl font-extrabold tracking-tight">{seller}</div>
          {store && store.name !== seller && <div className="text-sm font-semibold">{store.name}</div>}
          {store?.address && <div className="mt-0.5 whitespace-pre-line text-xs">{store.address}</div>}
          <div className="text-xs">{[store?.phone && `Phone: ${store.phone}`, store?.state_code && `State: ${stateName(store.state_code)}`].filter(Boolean).join(' · ')}</div>
          {inv.store_gstin && <div className="mt-1 font-mono text-xs font-bold">GSTIN: {inv.store_gstin}</div>}
        </div>
        <div className="shrink-0 text-right">
          <div className="text-lg font-extrabold uppercase tracking-wide">{title}</div>
          <div className="text-[9pt] font-semibold uppercase tracking-widest">{copy}</div>
          {inv.reverse_charge && <div className="mt-1 text-[9pt] font-semibold">Tax payable on reverse charge</div>}
          {inv.supply_category && <div className="text-[9pt]">{inv.supply_category.replace(/_/g, ' ')}</div>}
          {inv.status === 'cancelled' && <div className="mt-1 inline-block border-2 border-black px-2 py-0.5 text-xs font-extrabold uppercase">Cancelled</div>}
        </div>
      </header>

      {/* meta + buyer */}
      <section className="mt-3 grid grid-cols-2 gap-4 text-xs">
        <div className="border border-black p-2">
          <div className="mb-1 text-[8.5pt] font-bold uppercase tracking-widest">{buyerLabel}</div>
          <div className="text-sm font-bold">{inv.party_name}</div>
          {party?.address && <div className="whitespace-pre-line">{party.address}</div>}
          {(inv.party_phone || party?.phone) && <div>Phone: {inv.party_phone || party?.phone}</div>}
          {party?.email && <div>{party.email}</div>}
          {inv.party_gstin ? <div className="mt-1 font-mono font-bold">GSTIN: {inv.party_gstin}</div> : <div className="mt-1">Unregistered{purchase ? ' supplier' : ' / consumer'}</div>}
          {(inv.party_state || party?.state_code) && <div>State: {stateName(inv.party_state || party?.state_code)}</div>}
        </div>
        <div className="border border-black p-2">
          <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-0.5">
            <dt className="font-semibold">{title} No.</dt><dd className="font-mono font-bold">{inv.number}</dd>
            <dt className="font-semibold">Date</dt><dd>{fmtDate(inv.invoice_date)}</dd>
            {inv.due_date && <><dt className="font-semibold">Due date</dt><dd>{fmtDate(inv.due_date)}</dd></>}
            {inv.supplier_invoice_no && <><dt className="font-semibold">Supplier bill</dt><dd>{inv.supplier_invoice_no}{inv.supplier_invoice_date ? ` · ${fmtDate(inv.supplier_invoice_date)}` : ''}</dd></>}
            {isNote(inv.doc_type) && (inv.ref_invoice_id || refNumber) && <><dt className="font-semibold">Against</dt><dd className="font-mono">{refNumber ?? inv.ref_invoice_id}</dd></>}
            <dt className="font-semibold">Place of supply</dt><dd>{stateName(inv.place_of_supply)}</dd>
            <dt className="font-semibold">Supply type</dt><dd>{inter ? 'Inter-state (IGST)' : 'Intra-state (CGST + SGST)'}</dd>
            {inv.irn && <><dt className="font-semibold">IRN</dt><dd className="break-all font-mono text-[8pt]">{inv.irn}</dd></>}
          </dl>
        </div>
      </section>

      {/* lines */}
      <table className="mt-3 w-full border-collapse text-[9.5pt]">
        <thead>
          <tr className="border border-black bg-slate-100 text-[8.5pt] uppercase tracking-wide">
            <th className="border border-black px-1 py-1 text-left">S.No</th>
            <th className="border border-black px-1 py-1 text-left">Description of goods / services</th>
            <th className="border border-black px-1 py-1 text-left">HSN/SAC</th>
            <th className="border border-black px-1 py-1 text-right">Qty</th>
            <th className="border border-black px-1 py-1 text-right">Rate</th>
            {hasDisc && <th className="border border-black px-1 py-1 text-right">Disc.</th>}
            <th className="border border-black px-1 py-1 text-right">Taxable</th>
            {inter ? (
              <th className="border border-black px-1 py-1 text-right">IGST<br /><span className="font-normal normal-case">% · amount</span></th>
            ) : (
              <>
                <th className="border border-black px-1 py-1 text-right">CGST<br /><span className="font-normal normal-case">% · amount</span></th>
                <th className="border border-black px-1 py-1 text-right">SGST/UTGST<br /><span className="font-normal normal-case">% · amount</span></th>
              </>
            )}
            {hasCess && <th className="border border-black px-1 py-1 text-right">Cess</th>}
            <th className="border border-black px-1 py-1 text-right">Total</th>
          </tr>
        </thead>
        <tbody>
          {(inv.lines ?? []).map((l) => {
            const cat = l.tax_category && l.tax_category !== 'taxable' ? CATEGORY_LABEL[l.tax_category] : ''
            const rate = cat ? 0 : l.tax_rate_bp
            return (
              <tr key={l.line_no} className="align-top">
                <td className="border border-black px-1 py-1">{l.line_no}</td>
                <td className="border border-black px-1 py-1">
                  <div className="font-semibold">{l.name}</div>
                  <div className="text-[8pt]">{[l.batch_no && `Batch ${l.batch_no}`, cat, l.price_includes_tax ? 'incl. tax' : ''].filter(Boolean).join(' · ')}</div>
                </td>
                <td className="border border-black px-1 py-1 font-mono">{l.hsn || '—'}</td>
                <td className="border border-black px-1 py-1 text-right font-mono tabular-nums">{fmtQty(l.qty)} <span className="text-[8pt]">{l.unit}</span></td>
                <td className="border border-black px-1 py-1 text-right font-mono tabular-nums">{formatINR(l.unit_price_paise)}</td>
                {hasDisc && <td className="border border-black px-1 py-1 text-right font-mono tabular-nums">{l.discount_paise ? formatINR(l.discount_paise) : '—'}</td>}
                <td className="border border-black px-1 py-1 text-right font-mono tabular-nums">{formatINR(l.taxable_paise)}</td>
                {inter ? (
                  <td className="border border-black px-1 py-1 text-right font-mono tabular-nums">{formatRate(rate)}<br />{formatINR(l.igst_paise)}</td>
                ) : (
                  <>
                    <td className="border border-black px-1 py-1 text-right font-mono tabular-nums">{formatRate(rate / 2)}<br />{formatINR(l.cgst_paise)}</td>
                    <td className="border border-black px-1 py-1 text-right font-mono tabular-nums">{formatRate(rate / 2)}<br />{formatINR(l.sgst_paise)}</td>
                  </>
                )}
                {hasCess && <td className="border border-black px-1 py-1 text-right font-mono tabular-nums">{formatINR(l.cess_paise)}</td>}
                <td className="border border-black px-1 py-1 text-right font-mono tabular-nums font-semibold">{formatINR(l.total_paise)}</td>
              </tr>
            )
          })}
          <tr className="bg-slate-50 font-semibold">
            <td className="border border-black px-1 py-1" colSpan={3}>Total</td>
            <td className="border border-black px-1 py-1 text-right font-mono tabular-nums">{fmtQty((inv.lines ?? []).reduce((a, l) => a + l.qty, 0))}</td>
            <td className="border border-black px-1 py-1" />
            {hasDisc && <td className="border border-black px-1 py-1 text-right font-mono tabular-nums">{formatINR(inv.discount_paise)}</td>}
            <td className="border border-black px-1 py-1 text-right font-mono tabular-nums">{formatINR(inv.taxable_paise)}</td>
            {inter ? (
              <td className="border border-black px-1 py-1 text-right font-mono tabular-nums">{formatINR(inv.igst_paise)}</td>
            ) : (
              <>
                <td className="border border-black px-1 py-1 text-right font-mono tabular-nums">{formatINR(inv.cgst_paise)}</td>
                <td className="border border-black px-1 py-1 text-right font-mono tabular-nums">{formatINR(inv.sgst_paise)}</td>
              </>
            )}
            {hasCess && <td className="border border-black px-1 py-1 text-right font-mono tabular-nums">{formatINR(inv.cess_paise)}</td>}
            <td className="border border-black px-1 py-1 text-right font-mono tabular-nums">{formatINR(inv.taxable_paise + tax)}</td>
          </tr>
        </tbody>
      </table>
      <div className="sr-only">{cols} columns</div>

      {/* tax summary + totals */}
      <section className="mt-3 grid grid-cols-[1fr_72mm] gap-4 text-xs">
        <div>
          <table className="w-full border-collapse">
            <thead>
              <tr className="bg-slate-100 text-[8.5pt] uppercase">
                <th className="border border-black px-1 py-0.5 text-left">Tax rate</th>
                <th className="border border-black px-1 py-0.5 text-right">Taxable value</th>
                {inter ? <th className="border border-black px-1 py-0.5 text-right">IGST</th> : <><th className="border border-black px-1 py-0.5 text-right">CGST</th><th className="border border-black px-1 py-0.5 text-right">SGST</th></>}
                {hasCess && <th className="border border-black px-1 py-0.5 text-right">Cess</th>}
                <th className="border border-black px-1 py-0.5 text-right">Total tax</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={`${r.tax_rate_bp}:${r.tax_category}`}>
                  <td className="border border-black px-1 py-0.5">{r.tax_category !== 'taxable' ? CATEGORY_LABEL[r.tax_category] : formatRate(r.tax_rate_bp)}</td>
                  <td className="border border-black px-1 py-0.5 text-right font-mono tabular-nums">{formatINR(r.taxable_paise)}</td>
                  {inter ? <td className="border border-black px-1 py-0.5 text-right font-mono tabular-nums">{formatINR(r.igst_paise)}</td> : <><td className="border border-black px-1 py-0.5 text-right font-mono tabular-nums">{formatINR(r.cgst_paise)}</td><td className="border border-black px-1 py-0.5 text-right font-mono tabular-nums">{formatINR(r.sgst_paise)}</td></>}
                  {hasCess && <td className="border border-black px-1 py-0.5 text-right font-mono tabular-nums">{formatINR(r.cess_paise)}</td>}
                  <td className="border border-black px-1 py-0.5 text-right font-mono tabular-nums">{formatINR(r.cgst_paise + r.sgst_paise + r.igst_paise + r.cess_paise)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <div className="mt-2 border border-black p-2">
            <div className="text-[8.5pt] font-bold uppercase tracking-widest">Amount in words</div>
            <div className="font-semibold">{inv.amount_in_words}</div>
          </div>
          {(inv.tenders?.length ?? 0) > 0 && (
            <div className="mt-2 border border-black p-2">
              <div className="text-[8.5pt] font-bold uppercase tracking-widest">{purchase ? 'Payment made' : 'Payment received'}</div>
              <ul>
                {inv.tenders!.map((t, i) => (
                  <li key={i} className="flex justify-between"><span>{TENDER_LABEL[t.mode]}{t.reference ? ` · ${t.reference}` : ''}</span><span className="font-mono tabular-nums">{formatINR(t.amount_paise)}</span></li>
                ))}
              </ul>
            </div>
          )}
        </div>
        <div>
          <table className="w-full">
            <tbody>
              <Row l="Taxable value" v={inv.taxable_paise} />
              {inv.discount_paise > 0 && <Row l="Discount (included)" v={inv.discount_paise} muted />}
              {inter ? <Row l="IGST" v={inv.igst_paise} /> : <><Row l="CGST" v={inv.cgst_paise} /><Row l="SGST/UTGST" v={inv.sgst_paise} /></>}
              {hasCess && <Row l="Cess" v={inv.cess_paise} />}
              {!!inv.tcs_paise && <Row l="TCS 206C(1H)" v={inv.tcs_paise} />}
              {inv.round_off_paise !== 0 && <Row l="Round off" v={inv.round_off_paise} signed />}
              <tr className="border-t-2 border-black text-sm font-extrabold">
                <td className="py-1">Grand total</td>
                <td className="py-1 text-right font-mono tabular-nums">{formatINR(inv.grand_total_paise)}</td>
              </tr>
              {inv.paid_paise > 0 && <Row l={purchase ? 'Paid' : 'Received'} v={inv.paid_paise} />}
              {!!inv.change_paise && <Row l="Change returned" v={inv.change_paise} muted />}
              {!!inv.credited_paise && <Row l="Credited by notes" v={inv.credited_paise} muted />}
              {!isNote(inv.doc_type) && <tr className="font-bold"><td className="py-0.5">Balance due</td><td className="py-0.5 text-right font-mono tabular-nums">{formatINR(inv.balance_paise)}</td></tr>}
            </tbody>
          </table>
          <div className="mt-8 text-right">
            <div className="text-xs font-semibold">for {seller}</div>
            <div className="mt-10 border-t border-black pt-1 text-[8.5pt]">Authorised signatory</div>
          </div>
        </div>
      </section>

      <footer className="mt-4 border-t border-black pt-2 text-[8.5pt]">
        {inv.notes && <p className="mb-1"><span className="font-bold">Notes / terms: </span>{inv.notes}</p>}
        {inv.cancel_reason && <p className="mb-1"><span className="font-bold">Cancelled: </span>{inv.cancel_reason}{inv.cancelled_at ? ` (${fmtDate(inv.cancelled_at, true)})` : ''}</p>}
        <p><span className="font-bold">Declaration: </span>We declare that this {title.toLowerCase()} shows the actual price of the goods/services described and that all particulars are true and correct.{tax === 0 && inv.doc_type === 'sale' ? ' No GST is charged on this supply.' : ''}</p>
        <p className="mt-1">Subject to {store?.state_code ? stateName(store.state_code).replace(/\s*\(\d+\)$/, '') : 'local'} jurisdiction. E. & O.E.</p>
      </footer>
    </article>
  )
}

function Row({ l, v, muted, signed }: { l: string; v: number; muted?: boolean; signed?: boolean }) {
  return (
    <tr className={muted ? 'text-slate-600' : ''}>
      <td className="py-0.5">{l}</td>
      <td className="py-0.5 text-right font-mono tabular-nums">{signed && v > 0 ? '+' : ''}{formatINR(v)}</td>
    </tr>
  )
}
