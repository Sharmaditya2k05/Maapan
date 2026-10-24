'use client'

import { useState } from 'react'
import { CheckCircle2, Lock, Trash2 } from 'lucide-react'
import { Button, Empty, ErrorBox, Field, IconButton, Input, MoneyInput, Select, Table, Textarea, useToast } from '@/components/ui'
import { usePerm, useSave, useStore } from '@/lib/hooks'
import { errorMessage } from '@/lib/api'
import { PERMS } from '@/lib/rbac'
import { storeName } from '@/lib/stock/scope'
import type { Adjustment, AdjustmentInput, AdjustmentReason, ItemFull } from '@/lib/types.stock'
import { ItemPicker } from './ItemPicker'
import { MovementTable } from './MovementTable'
import { StoreSelect } from './StoreSelect'

interface Line { key: number; item: ItemFull; qty: string; unit: string; batch_no: string; expiry_date: string; unit_cost: number }

/** Manual stock adjustment / expiry write-off → POST /bhandar/v1/stock/adjustments, then the movements it posted. */
export function StockAdjustments() {
  const { storeId, stores } = useStore()
  const canDo = usePerm()
  const toast = useToast()
  const [store, setStore] = useState(storeId ?? '')
  const [reason, setReason] = useState<AdjustmentReason>('adjustment')
  const [note, setNote] = useState('')
  const [lines, setLines] = useState<Line[]>([])
  const [err, setErr] = useState('')
  const [result, setResult] = useState<Adjustment | null>(null)
  const save = useSave<Adjustment, AdjustmentInput>('/bhandar/v1/stock/adjustments', { invalidate: ['/bhandar/v1/stock', '/bhandar/v1/items', '/bhandar/v1/analytics', '/bhandar/v1/batches'], silent: true })

  if (!canDo(PERMS.stockManage)) return <Empty icon={Lock} title="Stock managers only" text="Adjustments need the stock:manage permission." />

  const add = (item: ItemFull) => setLines((ls) => [...ls, { key: Date.now() + Math.random(), item, qty: '', unit: item.unit, batch_no: '', expiry_date: '', unit_cost: item.purchase_price_paise }])
  const update = (key: number, patch: Partial<Line>) => setLines((ls) => ls.map((l) => (l.key === key ? { ...l, ...patch } : l)))
  const reset = () => { setLines([]); setNote(''); setErr(''); setResult(null) }

  const submit = async () => {
    setErr('')
    if (!note.trim()) return setErr('A note explaining the adjustment is required.')
    if (lines.length === 0) return setErr('Add at least one line.')
    for (const l of lines) {
      const q = Number(l.qty)
      if (!Number.isFinite(q) || q === 0) return setErr(`${l.item.name}: quantity cannot be zero.`)
      if (reason === 'expiry_writeoff' && q > 0) return setErr(`${l.item.name}: an expiry write-off must be a negative quantity.`)
      if (l.item.track_batches && !l.batch_no.trim()) return setErr(`${l.item.name} is batch-tracked: enter the batch number.`)
    }
    const body: AdjustmentInput = {
      store_id: store || undefined, reason, note: note.trim(),
      lines: lines.map((l) => ({
        item_id: l.item.id, qty: Number(l.qty), unit: l.unit !== l.item.unit ? l.unit : undefined,
        batch_no: l.batch_no.trim() || undefined, expiry_date: l.expiry_date || undefined,
        unit_cost_paise: Number(l.qty) > 0 && l.unit_cost > 0 ? l.unit_cost : undefined,
      })),
    }
    try {
      const adj = await save.mutateAsync(body)
      setResult(adj)
      toast(`Posted ${adj.movements.length} movement${adj.movements.length === 1 ? '' : 's'}`)
    } catch (e) { setErr(errorMessage(e)) }
  }

  if (result) {
    return (
      <div className="p-3 sm:p-4">
        <div className="mb-4 flex items-start gap-3 rounded-xl bg-emerald-50 px-4 py-3 text-sm text-emerald-800">
          <CheckCircle2 size={18} className="mt-0.5 shrink-0" />
          <div><div className="font-semibold">Adjustment posted at {storeName(stores, result.store_id)}</div><div className="text-emerald-700">{result.note}</div></div>
        </div>
        {result.movements.length === 0 ? <ErrorBox message="No movement was recorded: every line converted to a zero quantity." /> : <MovementTable rows={result.movements} showItem showRunning={false} />}
        <div className="mt-4 flex justify-end"><Button onClick={reset}>New adjustment</Button></div>
      </div>
    )
  }

  return (
    <div className="space-y-4 p-3 sm:p-4">
      <div className="grid gap-4 sm:grid-cols-3">
        <Field label="Store"><StoreSelect value={store} onChange={setStore} className="sm:w-full" /><span className="mt-1 block text-xs text-slate-500">{stores.length <= 1 ? storeName(stores, store || storeId) : ''}</span></Field>
        <Field label="Reason">
          <Select value={reason} onChange={(e) => setReason(e.target.value as AdjustmentReason)}>
            <option value="adjustment">Adjustment (count correction, damage, theft)</option>
            <option value="expiry_writeoff">Expiry write-off (negative only)</option>
          </Select>
        </Field>
        <Field label="Note (required)" className="sm:col-span-3"><Textarea rows={2} value={note} onChange={(e) => setNote(e.target.value)} placeholder="Physical count on 24 Oct: 3 packs short" /></Field>
      </div>

      <Field label="Add item"><ItemPicker onPick={add} /></Field>

      {lines.length > 0 && (
        <Table>
          <thead className="bg-slate-50/60"><tr><th className="th">Item</th><th className="th">Unit</th><th className="th text-right">Qty (± )</th><th className="th">Batch</th><th className="th">Expiry</th><th className="th text-right">Unit cost</th><th className="th w-12" /></tr></thead>
          <tbody className="divide-y divide-slate-100">
            {lines.map((l) => {
              const q = Number(l.qty)
              return (
                <tr key={l.key}>
                  <td className="td"><div className="font-semibold">{l.item.name}</div><div className="text-xs text-slate-500">{l.item.sku || l.item.barcode}</div></td>
                  <td className="td">
                    <Select value={l.unit} onChange={(e) => update(l.key, { unit: e.target.value })} className="input-sm w-24" aria-label="Unit">
                      <option value={l.item.unit}>{l.item.unit}</option>
                      {l.item.alt_units?.map((a) => <option key={a.unit} value={a.unit}>{a.unit} (×{a.factor})</option>)}
                    </Select>
                  </td>
                  <td className="td text-right"><Input value={l.qty} onChange={(e) => update(l.key, { qty: e.target.value.replace(/[^0-9.-]/g, '') })} inputMode="decimal" className={`input-sm num w-28 text-right ${q < 0 ? 'text-rose-600' : q > 0 ? 'text-emerald-700' : ''}`} placeholder={reason === 'expiry_writeoff' ? '-1' : '+1 / -1'} aria-label={`Quantity for ${l.item.name}`} autoFocus /></td>
                  <td className="td"><Input value={l.batch_no} onChange={(e) => update(l.key, { batch_no: e.target.value })} className="input-sm w-28 font-mono" placeholder={l.item.track_batches ? 'required' : 'optional'} aria-label="Batch number" /></td>
                  <td className="td"><Input type="date" value={l.expiry_date} onChange={(e) => update(l.key, { expiry_date: e.target.value })} className="input-sm w-36" disabled={!l.batch_no.trim()} aria-label="Expiry date" /></td>
                  <td className="td text-right">{q > 0 ? <MoneyInput value={l.unit_cost} onChange={(v) => update(l.key, { unit_cost: v })} className="input-sm w-28" aria-label="Unit cost" /> : <span className="text-xs text-slate-400">at avg cost</span>}</td>
                  <td className="td"><IconButton icon={Trash2} label={`Remove ${l.item.name}`} onClick={() => setLines((ls) => ls.filter((x) => x.key !== l.key))} /></td>
                </tr>
              )
            })}
          </tbody>
        </Table>
      )}
      {lines.length === 0 && <p className="text-sm text-slate-500">Positive quantities add stock at the unit cost you enter; negative quantities remove stock at the current average cost. Batch-tracked items need a batch number.</p>}

      {err && <ErrorBox message={err} />}
      <div className="flex justify-end gap-2">
        <Button variant="secondary" onClick={reset} disabled={lines.length === 0 && !note}>Clear</Button>
        <Button onClick={submit} loading={save.isPending} disabled={lines.length === 0}>Post adjustment</Button>
      </div>
    </div>
  )
}
