'use client'

/**
 * /app/returns/new?ref=<original id> — pick quantities from the original
 * document; posts a credit note (sale_return) or debit note (purchase_return).
 * The server copies party, prices and rates and enforces the returnable qty;
 * already-returned quantities are estimated from posted notes for guidance.
 */
import { Suspense, useEffect, useMemo, useState } from 'react'
import Link from 'next/link'
import { useRouter, useSearchParams } from 'next/navigation'
import { Receipt, Undo2 } from 'lucide-react'
import { Badge, Button, Card, Empty, ErrorBox, Money, PageHeader, Skeleton, Textarea, useToast } from '@/components/ui'
import { fmtDate, fmtQty, useGet, useInvalidate, useList, usePerm, useStore } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { post } from '@/lib/api'
import { deviceId } from '@/lib/session'
import { formatRate } from '@/lib/money'
import type { InvoiceLine } from '@/lib/types'
import type { InvoiceFull, PosCreateInvoiceInput } from '@/lib/types.pos'
import { cartTotals, round3 } from '@/lib/pos/cart'
import { cleanCreateInput, lineToCart } from '@/lib/pos/invoice'
import { PreviewPanel } from '@/components/invoice/PreviewPanel'

export default function NewReturnPage() {
  return <Suspense fallback={<Skeleton />}><NewReturn /></Suspense>
}

const lineKey = (l: Pick<InvoiceLine, 'item_id' | 'name'>) => (l.item_id ? `id:${l.item_id}` : `name:${l.name.trim().toLowerCase()}`)

function NewReturn() {
  const router = useRouter()
  const canDo = usePerm()
  const toast = useToast()
  const invalidate = useInvalidate()
  const { store } = useStore()
  const ref = useSearchParams().get('ref')
  const orig = useGet<InvoiceFull>(ref ? `/aadesh/v1/invoices/${ref}` : null)
  const o = orig.data
  const noteType = o?.doc_type === 'purchase' ? 'purchase_return' : 'sale_return'
  // Posted notes of the same party since the original's date, narrowed to this original client-side.
  const notes = useList<InvoiceFull>(o ? '/aadesh/v1/invoices' : null, { doc_type: noteType, party_id: o?.party_id, from: o?.invoice_date, status: 'posted', include: 'lines', limit: 200 })
  const returned = useMemo(() => {
    const m: Record<string, number> = {}
    for (const n of notes.data?.items ?? []) {
      if (n.ref_invoice_id !== o?.id) continue
      for (const l of n.lines ?? []) m[lineKey(l)] = (m[lineKey(l)] ?? 0) + l.qty
    }
    return m
  }, [notes.data, o?.id])

  const [qty, setQty] = useState<Record<number, string>>({})
  const [reason, setReason] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  useEffect(() => { setQty({}) }, [o?.id])

  const lines = o?.lines ?? []
  const remaining = (l: InvoiceLine) => round3(Math.max(0, l.qty - (returned[lineKey(l)] ?? 0)))
  const chosen = lines.map((l) => ({ l, q: round3(Number(qty[l.line_no]) || 0) })).filter((x) => x.q > 0)
  const preview = useMemo(() => {
    if (!o || chosen.length === 0) return null
    try { return cartTotals(chosen.map(({ l, q }) => lineToCart({ ...l, qty: q, discount_paise: 0, discount_bp: 0 })), o.store_state, o.place_of_supply) } catch { return null }
  }, [o, chosen])

  const submit = async () => {
    if (!o) return
    setErr(null)
    if (chosen.length === 0) return setErr('Enter a quantity for at least one line')
    for (const { l, q } of chosen) if (q > remaining(l) + 1e-9) return setErr(`${l.name}: only ${fmtQty(remaining(l))} ${l.unit} can still be returned`)
    const input: PosCreateInvoiceInput = cleanCreateInput({
      doc_type: noteType, store_id: o.store_id, ref_invoice_id: o.id, party_id: o.party_id,
      lines: chosen.map(({ l, q }) => ({ item_id: l.item_id, name: l.name, hsn: l.hsn, unit: l.unit, batch_no: l.batch_no, qty: q, unit_price_paise: l.unit_price_paise, tax_rate_bp: l.tax_rate_bp, cess_rate_bp: l.cess_rate_bp, price_includes_tax: l.price_includes_tax, tax_category: l.tax_category })),
      notes: reason.trim() || undefined, client_uuid: crypto.randomUUID(), device_id: deviceId(),
    })
    setSaving(true)
    try {
      const inv = await post<InvoiceFull>('/aadesh/v1/invoices', input)
      invalidate('/aadesh/v1/invoices', '/bahi/v1/parties', '/bhandar/v1/stock')
      toast(`${noteType === 'sale_return' ? 'Credit note' : 'Debit note'} ${inv.number} posted`)
      router.replace(`/app/returns/${inv.id}`)
    } catch (e) {
      setErr((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  if (!canDo(PERMS.invoiceCreate)) return <Card><Empty icon={Receipt} title="Not allowed" text="Your role cannot post returns." /></Card>
  if (!ref) return <Card><Empty icon={Undo2} title="Which document is being returned?" text="Open the sales invoice or purchase bill and choose Return." action={<Link href="/app/sales"><Button>Go to sales</Button></Link>} /></Card>
  if (orig.isLoading) return <Skeleton />
  if (orig.error || !o) return <ErrorBox message={(orig.error as Error)?.message ?? 'Original document not found'} retry={() => orig.refetch()} />
  const isSale = o.doc_type === 'sale'
  if (o.status !== 'posted' || (!isSale && o.doc_type !== 'purchase')) return <Card><Empty icon={Receipt} title="Cannot return against this document" text="Only posted sales invoices and purchase bills can be returned." /></Card>
  if (store && o.store_id !== store.id) return <Card><Empty icon={Receipt} title="Wrong store" text="A return must be made at the store that issued the original. Switch store in the top bar." /></Card>

  return (
    <>
      <PageHeader back={() => router.back()} title={isSale ? 'Credit note (sales return)' : 'Debit note (return to supplier)'}
        subtitle={<>Against <span className="font-mono font-semibold">{o.number}</span> · {o.party_name} · {fmtDate(o.invoice_date)} · <Money paise={o.grand_total_paise} />{o.credited_paise ? <> · already credited <Money paise={o.credited_paise} /></> : null}</>} />
      <div className="grid gap-5 xl:grid-cols-[minmax(0,1fr)_320px] sm:gap-6">
        <div className="space-y-5">
          <Card padded={false} title="Lines on the original" actions={<Button size="sm" variant="ghost" onClick={() => setQty(Object.fromEntries(lines.map((l) => [l.line_no, String(remaining(l))])))}>Return everything</Button>}>
            <ul className="divide-y divide-slate-100 md:hidden">
              {lines.map((l) => (
                <li key={l.line_no} className="flex items-center gap-3 px-4 py-3">
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-sm font-semibold">{l.name}</div>
                    <div className="text-xs text-slate-500">Sold {fmtQty(l.qty)} · returned {fmtQty(returned[lineKey(l)] ?? 0)} · left {fmtQty(remaining(l))} {l.unit} · <Money paise={l.unit_price_paise} /></div>
                  </div>
                  <input type="number" inputMode="decimal" min={0} max={remaining(l)} step="any" className="input input-sm num w-24" value={qty[l.line_no] ?? ''} disabled={remaining(l) <= 0}
                    onChange={(e) => setQty((p) => ({ ...p, [l.line_no]: e.target.value }))} aria-label={`Return quantity for ${l.name}`} />
                </li>
              ))}
            </ul>
            <div className="hidden overflow-x-auto md:block">
              <table className="w-full min-w-[640px]">
                <thead className="bg-slate-50/60"><tr><th className="th">Item</th><th className="th text-right">Sold</th><th className="th text-right">Returned</th><th className="th text-right">Left</th><th className="th text-right">Price</th><th className="th">GST</th><th className="th w-32">Return qty</th></tr></thead>
                <tbody className="divide-y divide-slate-100">
                  {lines.map((l) => (
                    <tr key={l.line_no}>
                      <td className="td"><div className="font-semibold">{l.name}</div><div className="text-xs text-slate-500">{[l.hsn && `HSN ${l.hsn}`, l.batch_no && `Batch ${l.batch_no}`].filter(Boolean).join(' · ')}</div></td>
                      <td className="td num text-right">{fmtQty(l.qty)} {l.unit}</td>
                      <td className="td num text-right text-slate-500">{fmtQty(returned[lineKey(l)] ?? 0)}</td>
                      <td className="td num text-right font-semibold">{fmtQty(remaining(l))}</td>
                      <td className="td num text-right"><Money paise={l.unit_price_paise} /></td>
                      <td className="td text-xs text-slate-500">{formatRate(l.tax_rate_bp)}</td>
                      <td className="td"><input type="number" inputMode="decimal" min={0} max={remaining(l)} step="any" className="input input-sm num" value={qty[l.line_no] ?? ''} disabled={remaining(l) <= 0}
                        onChange={(e) => setQty((p) => ({ ...p, [l.line_no]: e.target.value }))} aria-label={`Return quantity for ${l.name}`} /></td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            {notes.isLoading && <p className="px-4 py-2 text-xs text-slate-500">Checking earlier returns…</p>}
          </Card>
          <Card title="Reason / notes"><Textarea rows={3} value={reason} onChange={(e) => setReason(e.target.value)} placeholder="Damaged, wrong size, expired…" /></Card>
        </div>
        <div className="space-y-4 xl:sticky xl:top-20 xl:self-start">
          <PreviewPanel summary={preview} title={`${isSale ? 'Credit' : 'Debit'} note value`} extra={<p className="mt-3 text-xs text-slate-500">Prices and rates are copied from the original by the server; discounts on the original are not re-applied here, so the posted value may differ slightly.</p>} />
          {err && <ErrorBox message={err} />}
          <div className="flex items-center gap-2">
            <Badge tone="gray">{chosen.length} line{chosen.length === 1 ? '' : 's'}</Badge>
            <Button icon={Undo2} className="flex-1" onClick={submit} loading={saving} disabled={chosen.length === 0}>Post {isSale ? 'credit' : 'debit'} note</Button>
          </div>
        </div>
      </div>
    </>
  )
}
