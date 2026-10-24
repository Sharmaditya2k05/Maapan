'use client'

/**
 * BOM create / edit form (nirmaan bom.Input). Drafts only: active and archived
 * BOMs are immutable, so the page offers "New version" instead of this editor.
 */
import { useMemo, useState } from 'react'
import { Layers, Plus, Trash2 } from 'lucide-react'
import { Button, Card, ErrorBox, Field, IconButton, Input, MoneyInput, Select, Textarea, Toggle, useToast } from '@/components/ui'
import { useGet, useSave } from '@/lib/hooks'
import type { Bom, BomComponent, BomInput, BomOperation, BomSummary, NirmaanList } from '@/lib/types.more'
import { ItemPicker, type PickedItem } from './ItemPicker'

interface CompRow { key: number; item: PickedItem | null; qty: string; unit: string; scrap: string; sub: boolean; subBomId: string }
interface OpRow { key: number; name: string; cost_paise: number }

let seq = 1
const nextKey = () => seq++

const fromComponent = (c: BomComponent): CompRow => ({
  key: nextKey(), item: { id: c.item_id, name: c.item_name, unit: c.unit }, qty: String(c.qty), unit: c.unit,
  scrap: c.scrap_percent ? String(c.scrap_percent) : '', sub: c.is_sub_assembly, subBomId: c.sub_bom_id ?? '',
})
const fromOperation = (o: BomOperation): OpRow => ({ key: nextKey(), name: o.name, cost_paise: o.cost_paise })

const num = (s: string) => { const n = Number(s); return Number.isFinite(n) ? n : NaN }

export function BomEditor({ initial, onSaved, onCancel }: { initial?: Bom; onSaved: (b: Bom) => void; onCancel?: () => void }) {
  const toast = useToast()
  const [item, setItem] = useState<PickedItem | null>(initial ? { id: initial.item_id, name: initial.item_name, unit: initial.unit } : null)
  const [outputQty, setOutputQty] = useState(initial ? String(initial.output_qty) : '1')
  const [unit, setUnit] = useState(initial?.unit ?? '')
  const [notes, setNotes] = useState(initial?.notes ?? '')
  const [comps, setComps] = useState<CompRow[]>(initial?.components.map(fromComponent) ?? [{ key: nextKey(), item: null, qty: '1', unit: '', scrap: '', sub: false, subBomId: '' }])
  const [ops, setOps] = useState<OpRow[]>(initial?.operations.map(fromOperation) ?? [])
  const [error, setError] = useState<string | null>(null)

  const save = useSave<Bom, BomInput>(initial ? `/nirmaan/v1/boms/${initial.id}` : '/nirmaan/v1/boms', {
    method: initial ? 'PUT' : 'POST', invalidate: ['/nirmaan/v1/boms'], silent: true,
  })

  const updComp = (key: number, patch: Partial<CompRow>) => setComps((rows) => rows.map((r) => (r.key === key ? { ...r, ...patch } : r)))
  const updOp = (key: number, patch: Partial<OpRow>) => setOps((rows) => rows.map((r) => (r.key === key ? { ...r, ...patch } : r)))

  const usedIds = useMemo(() => [item?.id, ...comps.map((c) => c.item?.id)].filter((x): x is string => !!x), [item, comps])

  // Mirrors bom.Input.Validate so the common mistakes never reach the server.
  const validate = (): BomInput | string => {
    if (!item) return 'Pick the finished item.'
    const oq = num(outputQty)
    if (!(oq > 0)) return 'Output quantity must be positive.'
    if (comps.length === 0 || comps.length > 300) return 'A BOM needs 1 to 300 components.'
    const seen = new Set<string>()
    const components: BomComponent[] = []
    for (let i = 0; i < comps.length; i++) {
      const c = comps[i]
      if (!c.item) return `Component ${i + 1}: pick an item.`
      if (c.item.id === item.id) return `Component ${i + 1} is the finished item itself.`
      const q = num(c.qty)
      if (!(q > 0)) return `Component ${i + 1}: quantity must be positive.`
      const scrap = c.scrap === '' ? 0 : num(c.scrap)
      if (!(scrap >= 0 && scrap < 100)) return `Component ${i + 1}: scrap must be 0 to <100 %.`
      if (seen.has(c.item.id)) return `Component ${i + 1} repeats an item; combine the quantities.`
      seen.add(c.item.id)
      components.push({
        item_id: c.item.id, item_name: c.item.name, qty: q, unit: (c.unit || c.item.unit || 'PCS').toUpperCase(),
        scrap_percent: scrap, is_sub_assembly: c.sub, sub_bom_id: c.sub && c.subBomId ? c.subBomId : undefined,
      })
    }
    const operations: BomOperation[] = []
    for (let i = 0; i < ops.length; i++) {
      const o = ops[i]
      if (!o.name.trim() || o.cost_paise < 0) return `Operation ${i + 1} needs a name and a non-negative cost.`
      operations.push({ name: o.name.trim(), cost_paise: o.cost_paise })
    }
    return { item_id: item.id, item_name: item.name, output_qty: oq, unit: (unit || item.unit || 'PCS').toUpperCase(), notes: notes.trim() || undefined, components, operations }
  }

  const submit = async () => {
    const body = validate()
    if (typeof body === 'string') { setError(body); return }
    setError(null)
    try {
      const b = await save.mutateAsync(body)
      toast(initial ? 'BOM saved' : `BOM v${b.version} created as a draft`)
      onSaved(b)
    } catch (e) {
      setError((e as Error).message)
    }
  }

  return (
    <form onSubmit={(e) => { e.preventDefault(); void submit() }} className="space-y-4">
      <Card title="Finished item">
        <div className="grid gap-4 sm:grid-cols-[1fr_140px_140px]">
          <Field label="Item" hint={initial ? 'The item cannot change on an existing BOM.' : undefined}>
            {initial ? <Input value={initial.item_name} disabled readOnly /> : <ItemPicker value={item} onChange={(it) => { setItem(it); if (it && !unit) setUnit(it.unit) }} exclude={usedIds} label="Finished item" />}
          </Field>
          <Field label="Batch output qty">
            <Input inputMode="decimal" className="num" value={outputQty} onChange={(e) => setOutputQty(e.target.value)} required />
          </Field>
          <Field label="Unit">
            <Input value={unit} onChange={(e) => setUnit(e.target.value.toUpperCase())} placeholder={item?.unit || 'PCS'} maxLength={12} />
          </Field>
        </div>
      </Card>

      <Card title={<span className="inline-flex items-center gap-2"><Layers size={14} /> Components <span className="text-xs font-normal text-slate-500">(per batch of {outputQty || '…'} {unit || item?.unit || ''})</span></span>}
        actions={<Button size="sm" variant="secondary" icon={Plus} onClick={() => setComps((r) => [...r, { key: nextKey(), item: null, qty: '1', unit: '', scrap: '', sub: false, subBomId: '' }])}>Add component</Button>}
        padded={false}>
        <div className="hidden grid-cols-[1fr_90px_80px_80px_150px_40px] gap-2 border-b border-slate-100 px-4 py-2 text-[11px] font-semibold uppercase tracking-wider text-slate-500 lg:grid">
          <div>Item</div><div className="text-right">Qty</div><div>Unit</div><div className="text-right">Scrap %</div><div>Sub-assembly</div><div />
        </div>
        <ul className="divide-y divide-slate-100">
          {comps.map((c, i) => (
            <li key={c.key} className="grid gap-2 px-4 py-3 lg:grid-cols-[1fr_90px_80px_80px_150px_40px] lg:items-center">
              <div className="flex items-start gap-2">
                <span className="mt-2 w-5 shrink-0 text-xs text-slate-400 lg:hidden">{i + 1}.</span>
                <div className="min-w-0 flex-1">
                  <ItemPicker small value={c.item} exclude={usedIds.filter((id) => id !== c.item?.id)} label={`Component ${i + 1} item`}
                    onChange={(it) => updComp(c.key, { item: it, unit: it ? it.unit : c.unit })} />
                </div>
              </div>
              <div className="grid grid-cols-3 gap-2 lg:contents">
                <label className="block lg:contents">
                  <span className="label lg:hidden">Qty</span>
                  <Input className="input-sm num text-right" inputMode="decimal" value={c.qty} onChange={(e) => updComp(c.key, { qty: e.target.value })} aria-label={`Component ${i + 1} quantity`} />
                </label>
                <label className="block lg:contents">
                  <span className="label lg:hidden">Unit</span>
                  <Input className="input-sm" value={c.unit} onChange={(e) => updComp(c.key, { unit: e.target.value.toUpperCase() })} placeholder={c.item?.unit || 'PCS'} maxLength={12} aria-label={`Component ${i + 1} unit`} />
                </label>
                <label className="block lg:contents">
                  <span className="label lg:hidden">Scrap %</span>
                  <Input className="input-sm num text-right" inputMode="decimal" value={c.scrap} placeholder="0" onChange={(e) => updComp(c.key, { scrap: e.target.value })} aria-label={`Component ${i + 1} scrap percent`} />
                </label>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <Toggle checked={c.sub} onChange={(v) => updComp(c.key, { sub: v, subBomId: v ? c.subBomId : '' })} label={<span className="text-xs">Sub-assembly</span>} />
                {c.sub && c.item && <SubBomSelect itemId={c.item.id} value={c.subBomId} onChange={(v) => updComp(c.key, { subBomId: v })} />}
              </div>
              <div className="flex justify-end">
                <IconButton icon={Trash2} label={`Remove component ${i + 1}`} disabled={comps.length === 1} onClick={() => setComps((r) => r.filter((x) => x.key !== c.key))} />
              </div>
            </li>
          ))}
        </ul>
      </Card>

      <Card title={<span>Operations &amp; overhead <span className="text-xs font-normal text-slate-500">(labour / overhead per batch)</span></span>}
        actions={<Button size="sm" variant="secondary" icon={Plus} onClick={() => setOps((r) => [...r, { key: nextKey(), name: '', cost_paise: 0 }])}>Add operation</Button>} padded={false}>
        {ops.length === 0 ? (
          <p className="px-4 py-4 text-sm text-slate-500">No operations. Add baking, packing, electricity… as a cost per batch; it is scaled by the run quantity.</p>
        ) : (
          <ul className="divide-y divide-slate-100">
            {ops.map((o, i) => (
              <li key={o.key} className="grid grid-cols-[1fr_auto] gap-2 px-4 py-3 sm:grid-cols-[1fr_180px_40px] sm:items-center">
                <Input className="input-sm col-span-2 sm:col-span-1" value={o.name} placeholder="Operation name (e.g. Baking)" onChange={(e) => updOp(o.key, { name: e.target.value })} aria-label={`Operation ${i + 1} name`} />
                <MoneyInput className="input-sm" value={o.cost_paise} onChange={(p) => updOp(o.key, { cost_paise: p })} aria-label={`Operation ${i + 1} cost per batch`} />
                <div className="flex justify-end"><IconButton icon={Trash2} label={`Remove operation ${i + 1}`} onClick={() => setOps((r) => r.filter((x) => x.key !== o.key))} /></div>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Card title="Notes">
        <Textarea rows={2} value={notes} onChange={(e) => setNotes(e.target.value)} placeholder="Process notes, tolerances, packaging…" aria-label="Notes" />
      </Card>

      {error && <ErrorBox message={error} />}
      <div className="flex flex-wrap justify-end gap-2">
        {onCancel && <Button variant="secondary" onClick={onCancel}>Cancel</Button>}
        <Button type="submit" loading={save.isPending}>{initial ? 'Save draft' : 'Create draft BOM'}</Button>
      </div>
    </form>
  )
}

/** Pin a specific BOM version of a sub-assembly, or leave on "active BOM". */
function SubBomSelect({ itemId, value, onChange }: { itemId: string; value: string; onChange: (v: string) => void }) {
  const boms = useGet<NirmaanList<BomSummary>>('/nirmaan/v1/boms', { item_id: itemId, limit: 50 })
  const rows = boms.data?.items ?? []
  return (
    <Select className="input-sm w-auto min-w-[120px]" value={value} onChange={(e) => onChange(e.target.value)} aria-label="Pinned sub-BOM version">
      <option value="">Active BOM</option>
      {rows.map((b) => <option key={b.id} value={b.id}>v{b.version} · {b.status}</option>)}
      {!boms.isLoading && rows.length === 0 && <option disabled value="__none">No BOM for this item yet</option>}
    </Select>
  )
}
