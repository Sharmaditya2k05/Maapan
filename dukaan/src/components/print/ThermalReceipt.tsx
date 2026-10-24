'use client'

/**
 * Counter receipt for 80 mm (72 mm printable) and 58 mm (48 mm) thermal printers.
 * Render inside `.print-root.print-thermal` (+ `.thermal-58`). Monospace, no colour.
 */
import { formatINR, formatRate } from '@/lib/money'
import { fmtDate, fmtQty } from '@/lib/hooks'
import type { Store } from '@/lib/types'
import type { InvoiceFull } from '@/lib/types.pos'
import { CATEGORY_LABEL, documentTitle, isWalkIn, summarizeByRate, TENDER_LABEL } from '@/lib/pos/invoice'

export function ThermalReceipt({ invoice: inv, store, businessName, width = 80, cashier, queued }: {
  invoice: InvoiceFull; store?: Store; businessName?: string; width?: 58 | 80; cashier?: string; queued?: boolean
}) {
  const narrow = width === 58
  const title = documentTitle(inv)
  const rows = summarizeByRate(inv.lines)
  const inter = inv.inter_state
  const items = inv.lines ?? []
  const totalQty = items.reduce((a, l) => a + l.qty, 0)
  const upi = (inv.tenders ?? []).filter((t) => t.mode === 'upi')

  return (
    <div className={`mx-auto w-full ${narrow ? 'max-w-[48mm] text-[8pt]' : 'max-w-[72mm] text-[9pt]'} font-mono leading-tight text-black`}>
      <div className="text-center">
        <div className="text-[11pt] font-bold uppercase">{businessName || store?.name}</div>
        {store && store.name !== businessName && <div>{store.name}</div>}
        {store?.address && <div className="whitespace-pre-line">{store.address}</div>}
        {store?.phone && <div>Ph: {store.phone}</div>}
        {inv.store_gstin && <div>GSTIN: {inv.store_gstin}</div>}
        <div className="mt-1 border-y border-dashed border-black py-0.5 font-bold uppercase">{title}{inv.status === 'cancelled' ? ' — CANCELLED' : ''}</div>
      </div>

      <div className="mt-1">
        <div className="flex justify-between"><span>Bill</span><span className="font-bold">{inv.number}</span></div>
        <div className="flex justify-between"><span>Date</span><span>{fmtDate(inv.created_at || inv.invoice_date, true)}</span></div>
        {cashier && <div className="flex justify-between"><span>Cashier</span><span className="truncate pl-2">{cashier}</span></div>}
        {!isWalkIn(inv) && (
          <>
            <div className="flex justify-between gap-2"><span>Customer</span><span className="truncate text-right">{inv.party_name}</span></div>
            {inv.party_phone && <div className="flex justify-between"><span>Phone</span><span>{inv.party_phone}</span></div>}
            {inv.party_gstin && <div className="flex justify-between"><span>GSTIN</span><span>{inv.party_gstin}</span></div>}
          </>
        )}
        {inter && <div className="flex justify-between"><span>Place of supply</span><span>{inv.place_of_supply} (IGST)</span></div>}
      </div>

      <div className="mt-1 border-t border-dashed border-black" />
      <div className="flex justify-between font-bold"><span>Item</span><span>Amount</span></div>
      {items.map((l) => (
        <div key={l.line_no} className="mt-0.5">
          <div className="break-words">{l.name}{l.batch_no ? ` [${l.batch_no}]` : ''}</div>
          <div className="flex justify-between pl-2">
            <span>{fmtQty(l.qty)} {l.unit} × {formatINR(l.unit_price_paise)}{l.discount_paise ? ` −${formatINR(l.discount_paise)}` : ''}{l.tax_category && l.tax_category !== 'taxable' ? ` ${CATEGORY_LABEL[l.tax_category]}` : l.tax_rate_bp ? ` @${formatRate(l.tax_rate_bp)}` : ''}</span>
            <span className="shrink-0 pl-2 tabular-nums">{formatINR(l.total_paise)}</span>
          </div>
        </div>
      ))}
      <div className="mt-1 border-t border-dashed border-black" />
      <div className="flex justify-between"><span>Items {items.length} · Qty {fmtQty(totalQty)}</span></div>

      <div className="mt-1">
        <Row l="Taxable" v={inv.taxable_paise} />
        {inv.discount_paise > 0 && <Row l="Discount (incl.)" v={inv.discount_paise} />}
        {inter ? <Row l="IGST" v={inv.igst_paise} /> : <><Row l="CGST" v={inv.cgst_paise} /><Row l="SGST" v={inv.sgst_paise} /></>}
        {inv.cess_paise > 0 && <Row l="Cess" v={inv.cess_paise} />}
        {!!inv.tcs_paise && <Row l="TCS" v={inv.tcs_paise} />}
        {inv.round_off_paise !== 0 && <Row l="Round off" v={inv.round_off_paise} signed />}
        <div className="mt-0.5 flex justify-between border-y border-black py-0.5 text-[11pt] font-bold">
          <span>TOTAL</span><span className="tabular-nums">{formatINR(inv.grand_total_paise)}</span>
        </div>
      </div>

      {rows.length > 0 && (
        <table className="mt-1 w-full">
          <thead><tr className="text-left"><th className="font-normal">GST</th><th className="text-right font-normal">Taxable</th>{inter ? <th className="text-right font-normal">IGST</th> : <><th className="text-right font-normal">CGST</th><th className="text-right font-normal">SGST</th></>}</tr></thead>
          <tbody>
            {rows.map((r) => (
              <tr key={`${r.tax_rate_bp}:${r.tax_category}`}>
                <td>{r.tax_category !== 'taxable' ? CATEGORY_LABEL[r.tax_category] : formatRate(r.tax_rate_bp)}</td>
                <td className="text-right tabular-nums">{formatINR(r.taxable_paise)}</td>
                {inter ? <td className="text-right tabular-nums">{formatINR(r.igst_paise)}</td> : <><td className="text-right tabular-nums">{formatINR(r.cgst_paise)}</td><td className="text-right tabular-nums">{formatINR(r.sgst_paise)}</td></>}
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {(inv.tenders?.length ?? 0) > 0 && (
        <div className="mt-1 border-t border-dashed border-black pt-0.5">
          {inv.tenders!.map((t, i) => <Row key={i} l={`${TENDER_LABEL[t.mode]}${t.reference ? ` ${t.reference}` : ''}`} v={t.amount_paise} />)}
          {!!inv.change_paise && <Row l="Change" v={inv.change_paise} />}
          {inv.balance_paise > 0 && <div className="flex justify-between font-bold"><span>Balance due</span><span className="tabular-nums">{formatINR(inv.balance_paise)}</span></div>}
        </div>
      )}
      {upi.length > 0 && <div className="mt-1 text-center">Paid by UPI{upi[0].reference ? ` · Ref ${upi[0].reference}` : ''}</div>}
      {inv.notes && <div className="mt-1 break-words">{inv.notes}</div>}
      {queued && <div className="mt-1 text-center">* Saved on this device; uploads when online *</div>}

      <div className="mt-2 text-center">
        <div className="font-bold">Thank you, visit again!</div>
        <div>{inv.amount_in_words}</div>
        <div className="mt-1">Goods once sold are not taken back without this bill.</div>
      </div>
    </div>
  )
}

function Row({ l, v, signed }: { l: string; v: number; signed?: boolean }) {
  return <div className="flex justify-between"><span>{l}</span><span className="tabular-nums">{signed && v > 0 ? '+' : ''}{formatINR(v)}</span></div>
}
