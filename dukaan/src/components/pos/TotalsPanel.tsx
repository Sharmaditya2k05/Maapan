'use client'

/** Live totals under the cart with the bill-level actions. */
import { Banknote, FilePlus2, PauseCircle } from 'lucide-react'
import { Badge, Button, Kbd, Money } from '@/components/ui'
import { fmtQty } from '@/lib/hooks'
import type { GstSummary } from '@/lib/gst'
import type { CartLine } from '@/lib/types.pos'
import { cartQty } from '@/lib/pos/cart'

export function TotalsPanel({ lines, totals, error, interState, onTender, onHold, onNew, saving }: {
  lines: CartLine[]; totals: GstSummary; error: string | null; interState: boolean
  onTender: () => void; onHold: () => void; onNew: () => void; saving?: boolean
}) {
  const empty = lines.length === 0
  return (
    <div className="@container border-t border-slate-200 bg-white">
      <div className="grid gap-3 p-3 sm:p-4 @lg:grid-cols-[1fr_auto]">
        <dl className="grid grid-cols-1 gap-x-4 gap-y-1 text-sm @xs:grid-cols-2 @2xl:grid-cols-3 [&_dd]:whitespace-nowrap [&_dt]:pr-2">
          <div className="flex justify-between text-slate-600"><dt>Items</dt><dd className="num">{lines.length} · {fmtQty(cartQty(lines))}</dd></div>
          <div className="flex justify-between text-slate-600"><dt>Taxable</dt><dd><Money paise={totals.taxable_paise} /></dd></div>
          {totals.discount_paise > 0 && <div className="flex justify-between text-emerald-700"><dt>Discount</dt><dd>−<Money paise={totals.discount_paise} /></dd></div>}
          {interState
            ? <div className="flex justify-between text-slate-600"><dt>IGST</dt><dd><Money paise={totals.igst_paise} /></dd></div>
            : <><div className="flex justify-between text-slate-600"><dt>CGST</dt><dd><Money paise={totals.cgst_paise} /></dd></div><div className="flex justify-between text-slate-600"><dt>SGST</dt><dd><Money paise={totals.sgst_paise} /></dd></div></>}
          {totals.cess_paise > 0 && <div className="flex justify-between text-slate-600"><dt>Cess</dt><dd><Money paise={totals.cess_paise} /></dd></div>}
          {totals.round_off_paise !== 0 && <div className="flex justify-between text-slate-400"><dt>Round off</dt><dd><Money paise={totals.round_off_paise} signed /></dd></div>}
        </dl>
        <div className="flex items-end justify-between gap-4 @lg:flex-col @lg:items-end">
          <div className="text-right">
            <div className="flex items-center justify-end gap-2 text-xs text-slate-500">Grand total {interState && <Badge tone="blue">IGST</Badge>}</div>
            <Money paise={totals.grand_total_paise} className="block text-3xl font-extrabold tracking-tight text-ink sm:text-4xl" />
          </div>
        </div>
      </div>
      {error && <div className="mx-3 mb-3 rounded-xl bg-rose-50 px-3 py-2 text-sm text-rose-700 sm:mx-4">{error}</div>}
      <div className="flex flex-wrap gap-2 border-t border-slate-100 p-3 sm:p-4">
        <Button variant="secondary" icon={PauseCircle} onClick={onHold} disabled={empty} className="flex-1 @md:flex-none">Hold <Kbd>F4</Kbd></Button>
        <Button variant="ghost" icon={FilePlus2} onClick={onNew} className="flex-1 @md:flex-none">New <Kbd>Alt+N</Kbd></Button>
        <Button variant="gold" size="lg" icon={Banknote} onClick={onTender} disabled={empty || !!error} loading={saving} className="w-full @md:ml-auto @md:w-auto @md:min-w-56">Tenders &amp; save <Kbd>F2</Kbd></Button>
      </div>
    </div>
  )
}
