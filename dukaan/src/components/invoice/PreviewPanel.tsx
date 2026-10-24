'use client'

/** Live totals for the editors: the server's /preview summary when available, the local computeGST result otherwise. */
import { Card, Money, Spinner } from '@/components/ui'
import type { GstSummary } from '@/lib/gst'
import type { ServerSummary } from '@/lib/types.pos'

export function PreviewPanel({ summary, words, loading, error, title = 'Summary', extra }: {
  summary: ServerSummary | GstSummary | null
  words?: string
  loading?: boolean
  error?: string | null
  title?: string
  extra?: React.ReactNode
}) {
  const s = summary
  const srv = s && 'tax_paise' in s ? s : null
  return (
    <Card title={title} actions={loading ? <Spinner className="h-4 w-4" /> : undefined}>
      {error && <div className="mb-3 rounded-xl bg-rose-50 px-3 py-2 text-sm text-rose-700">{error}</div>}
      <dl className="space-y-2 text-sm">
        <Row l="Taxable value" v={s?.taxable_paise ?? 0} />
        {!!s?.discount_paise && <Row l="Discounts" v={s.discount_paise} muted />}
        {!!srv?.invoice_discount_paise && <Row l="of which invoice discount" v={srv.invoice_discount_paise} muted />}
        {s?.inter_state ? <Row l="IGST" v={s.igst_paise} /> : <><Row l="CGST" v={s?.cgst_paise ?? 0} /><Row l="SGST" v={s?.sgst_paise ?? 0} /></>}
        {!!s?.cess_paise && <Row l="Cess" v={s.cess_paise} />}
        {!!srv?.tcs_paise && <Row l="TCS" v={srv.tcs_paise} />}
        {!!s?.round_off_paise && <Row l="Round off" v={s.round_off_paise} muted signed />}
        {srv?.reverse_charge && <div className="text-xs text-amber-700">Reverse charge: tax is payable by the recipient and excluded from the total.</div>}
      </dl>
      <div className="mt-4 rounded-2xl bg-gradient-to-br from-brand-700 to-brand-900 p-4 text-white">
        <div className="text-xs text-brand-200">{srv?.bill_of_supply ? 'Bill of supply total' : 'Grand total'}</div>
        <Money paise={s?.grand_total_paise ?? 0} className="text-3xl font-extrabold" />
        {words && <div className="mt-1 text-[11px] leading-snug text-brand-200">{words}</div>}
      </div>
      {extra}
    </Card>
  )
}

function Row({ l, v, muted, signed }: { l: string; v: number; muted?: boolean; signed?: boolean }) {
  return <div className={`flex justify-between ${muted ? 'text-slate-400' : 'text-slate-600'}`}><dt>{l}</dt><dd><Money paise={v} signed={signed} className="font-medium text-ink" /></dd></div>
}
