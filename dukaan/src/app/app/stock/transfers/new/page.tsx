'use client'

import { useState } from 'react'
import { useRouter } from 'next/navigation'
import { ArrowRight, Lock, Trash2 } from 'lucide-react'
import { Button, Card, Empty, ErrorBox, Field, IconButton, Input, PageHeader, Select, Table, Textarea } from '@/components/ui'
import { usePerm, useSave, useStore } from '@/lib/hooks'
import { errorMessage } from '@/lib/api'
import { PERMS } from '@/lib/rbac'
import type { ItemFull, Transfer, TransferInput } from '@/lib/types.stock'
import { ItemPicker } from '@/components/stock/ItemPicker'

interface Line { key: number; item: ItemFull; qty: string; unit: string; batch_no: string }

export default function NewTransferPage() {
  const router = useRouter()
  const canDo = usePerm()
  const { storeId, stores } = useStore()
  const [from, setFrom] = useState(storeId ?? '')
  const [to, setTo] = useState(() => stores.find((s) => s.id !== storeId)?.id ?? '')
  const [notes, setNotes] = useState('')
  const [lines, setLines] = useState<Line[]>([])
  const [err, setErr] = useState('')
  const save = useSave<Transfer, TransferInput>('/bhandar/v1/transfers', { invalidate: ['/bhandar/v1/transfers'], silent: true })

  if (!canDo(PERMS.stockManage)) return <Empty icon={Lock} title="Stock managers only" text="Transfers need the stock:manage permission." />
  if (stores.length < 2) return <Empty icon={Lock} title="Only one store" text="Transfers move stock between two stores. Add another under Settings → Stores." />

  const update = (key: number, patch: Partial<Line>) => setLines((ls) => ls.map((l) => (l.key === key ? { ...l, ...patch } : l)))

  const submit = async () => {
    setErr('')
    if (!from || !to) return setErr('Choose the source and destination stores.')
    if (from === to) return setErr('Source and destination stores must differ.')
    if (lines.length === 0) return setErr('Add at least one item.')
    for (const l of lines) {
      const q = Number(l.qty)
      if (!Number.isFinite(q) || q <= 0) return setErr(`${l.item.name}: enter a positive quantity.`)
      if (l.item.track_batches && !l.batch_no.trim()) return setErr(`${l.item.name} is batch-tracked: enter the batch to move.`)
    }
    try {
      const t = await save.mutateAsync({
        from_store_id: from, to_store_id: to, notes: notes.trim() || undefined,
        lines: lines.map((l) => ({ item_id: l.item.id, qty: Number(l.qty), unit: l.unit !== l.item.unit ? l.unit : undefined, batch_no: l.batch_no.trim() || undefined })),
      })
      router.replace(`/app/stock/transfers/${t.id}`)
    } catch (e) { setErr(errorMessage(e)) }
  }

  return (
    <>
      <PageHeader title="New transfer" subtitle="Saved as a draft; stock moves when you dispatch it" back={() => router.push('/app/stock?tab=transfers')} />
      <div className="mx-auto max-w-4xl space-y-4">
        <Card title="Route">
          <div className="grid items-end gap-3 sm:grid-cols-[1fr_auto_1fr]">
            <Field label="From store"><Select value={from} onChange={(e) => setFrom(e.target.value)}>{stores.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</Select></Field>
            <ArrowRight className="mx-auto mb-3 hidden text-slate-400 sm:block" />
            <Field label="To store"><Select value={to} onChange={(e) => setTo(e.target.value)}><option value="">Choose…</option>{stores.filter((s) => s.id !== from).map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</Select></Field>
            <Field label="Notes" className="sm:col-span-3"><Textarea rows={2} value={notes} onChange={(e) => setNotes(e.target.value)} placeholder="Weekly replenishment" /></Field>
          </div>
        </Card>

        <Card title="Items" padded={false}>
          <div className="p-3 sm:p-4"><ItemPicker onPick={(item) => setLines((ls) => [...ls, { key: Date.now() + Math.random(), item, qty: '', unit: item.unit, batch_no: '' }])} autoFocus /></div>
          {lines.length > 0 && (
            <Table>
              <thead className="bg-slate-50/60"><tr><th className="th">Item</th><th className="th">Unit</th><th className="th text-right">Qty</th><th className="th">Batch</th><th className="th w-12" /></tr></thead>
              <tbody className="divide-y divide-slate-100">
                {lines.map((l) => (
                  <tr key={l.key}>
                    <td className="td"><div className="font-semibold">{l.item.name}</div><div className="text-xs text-slate-500">{l.item.sku || l.item.barcode}</div></td>
                    <td className="td">
                      <Select value={l.unit} onChange={(e) => update(l.key, { unit: e.target.value })} className="input-sm w-24" aria-label="Unit">
                        <option value={l.item.unit}>{l.item.unit}</option>
                        {l.item.alt_units?.map((a) => <option key={a.unit} value={a.unit}>{a.unit} (×{a.factor})</option>)}
                      </Select>
                    </td>
                    <td className="td text-right"><Input value={l.qty} onChange={(e) => update(l.key, { qty: e.target.value.replace(/[^0-9.]/g, '') })} inputMode="decimal" className="input-sm num w-28 text-right" aria-label={`Quantity for ${l.item.name}`} autoFocus /></td>
                    <td className="td"><Input value={l.batch_no} onChange={(e) => update(l.key, { batch_no: e.target.value })} className="input-sm w-32 font-mono" placeholder={l.item.track_batches ? 'required' : 'optional'} aria-label="Batch number" /></td>
                    <td className="td"><IconButton icon={Trash2} label={`Remove ${l.item.name}`} onClick={() => setLines((ls) => ls.filter((x) => x.key !== l.key))} /></td>
                  </tr>
                ))}
              </tbody>
            </Table>
          )}
        </Card>

        {err && <ErrorBox message={err} />}
        <div className="flex justify-end gap-2 pb-6">
          <Button variant="secondary" onClick={() => router.push('/app/stock?tab=transfers')}>Cancel</Button>
          <Button onClick={submit} loading={save.isPending} disabled={lines.length === 0}>Save draft</Button>
        </div>
      </div>
    </>
  )
}
