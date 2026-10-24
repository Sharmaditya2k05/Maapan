'use client'

/** Edit one bill line: price (never above MRP), discount as % or ₹, qty, batch. Cashiers are capped at 10 % unless a manager unlocks. */
import { useEffect, useState } from 'react'
import { Lock, Unlock } from 'lucide-react'
import { Button, Field, Input, Modal, Money, MoneyInput } from '@/components/ui'
import { formatRate } from '@/lib/money'
import type { CartLine } from '@/lib/types.pos'
import { CASHIER_MAX_DISCOUNT_BP, capPrice, discountAllowed, lineDiscount, lineGross, round3 } from '@/lib/pos/cart'

export function LineEditor({ line, open, onClose, onSave, onRemove, role, unlocked, onUnlock }: {
  line: CartLine | null
  open: boolean
  onClose: () => void
  onSave: (patch: Partial<CartLine>) => void
  onRemove: () => void
  role: string | undefined
  unlocked: boolean
  onUnlock: () => void
}) {
  const [price, setPrice] = useState(0)
  const [qty, setQty] = useState('1')
  const [mode, setMode] = useState<'pct' | 'amt'>('pct')
  const [pct, setPct] = useState('')
  const [amt, setAmt] = useState(0)
  const [batch, setBatch] = useState('')
  const [err, setErr] = useState<string | null>(null)

  useEffect(() => {
    if (!line || !open) return
    setPrice(line.unit_price_paise)
    setQty(String(line.qty))
    setMode(line.discount_paise > 0 && !line.discount_bp ? 'amt' : 'pct')
    setPct(line.discount_bp ? String(line.discount_bp / 100) : '')
    setAmt(line.discount_paise)
    setBatch(line.batch_no ?? '')
    setErr(null)
  }, [line, open])

  if (!line) return null
  const q = round3(Number(qty) || 0)
  const draft: CartLine = {
    ...line, qty: q || line.qty, unit_price_paise: capPrice(price, line.mrp_paise),
    discount_bp: mode === 'pct' ? Math.min(10000, Math.round((Number(pct) || 0) * 100)) : 0,
    discount_paise: mode === 'amt' ? Math.max(0, amt) : 0,
    batch_no: batch.trim() || undefined,
  }
  const gross = lineGross(draft)
  const disc = lineDiscount(draft)
  const allowed = discountAllowed(draft, role, unlocked)
  const tooMuch = disc > gross

  const save = () => {
    if (!(q > 0)) return setErr('Quantity must be more than zero')
    if (tooMuch) return setErr('Discount exceeds the line value')
    if (!allowed) return setErr(`Cashiers may discount up to ${formatRate(CASHIER_MAX_DISCOUNT_BP)} per line. Ask a manager to unlock.`)
    if (line.track_batches && !draft.batch_no) return setErr('This item is batch-tracked: enter the batch number')
    onSave({ qty: q, unit_price_paise: draft.unit_price_paise, discount_bp: draft.discount_bp, discount_paise: draft.discount_paise, batch_no: draft.batch_no })
    onClose()
  }

  return (
    <Modal open={open} onClose={onClose} title={line.name}
      footer={<><Button variant="ghost" className="mr-auto text-rose-600" onClick={() => { onRemove(); onClose() }}>Remove line</Button><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={save}>Save</Button></>}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Quantity" hint={line.unit}><Input inputMode="decimal" autoFocus value={qty} onChange={(e) => setQty(e.target.value.replace(/[^0-9.]/g, ''))} onKeyDown={(e) => e.key === 'Enter' && save()} /></Field>
        <Field label="Price per unit" hint={line.mrp_paise ? <>MRP <Money paise={line.mrp_paise} /> — price cannot exceed it</> : line.price_includes_tax ? 'Tax inclusive' : 'Tax exclusive'}>
          <MoneyInput value={price} onChange={(v) => setPrice(capPrice(v, line.mrp_paise))} max={line.mrp_paise ? line.mrp_paise / 100 : undefined} onKeyDown={(e) => e.key === 'Enter' && save()} />
        </Field>
        <Field label="Discount" className="sm:col-span-2">
          <div className="flex gap-2">
            <div className="inline-flex rounded-xl bg-slate-100 p-1 text-xs font-semibold">
              <button type="button" onClick={() => setMode('pct')} className={`rounded-lg px-3 py-1.5 cursor-pointer ${mode === 'pct' ? 'bg-white shadow-sm' : 'text-slate-500'}`}>%</button>
              <button type="button" onClick={() => setMode('amt')} className={`rounded-lg px-3 py-1.5 cursor-pointer ${mode === 'amt' ? 'bg-white shadow-sm' : 'text-slate-500'}`}>₹</button>
            </div>
            {mode === 'pct'
              ? <Input inputMode="decimal" value={pct} onChange={(e) => setPct(e.target.value.replace(/[^0-9.]/g, ''))} placeholder="0" className="num" aria-label="Discount percent" onKeyDown={(e) => e.key === 'Enter' && save()} />
              : <div className="flex-1"><MoneyInput value={amt} onChange={setAmt} aria-label="Discount amount" onKeyDown={(e) => e.key === 'Enter' && save()} /></div>}
          </div>
          <div className={`mt-1.5 flex items-center justify-between text-xs ${allowed ? 'text-slate-500' : 'text-rose-600'}`}>
            <span>Line: <Money paise={gross} /> − <Money paise={disc} /> = <Money paise={Math.max(0, gross - disc)} className="font-semibold" /></span>
            {role === 'cashier' && (unlocked
              ? <span className="inline-flex items-center gap-1 text-emerald-700"><Unlock size={12} /> Manager unlocked</span>
              : <button type="button" onClick={onUnlock} className="inline-flex items-center gap-1 font-semibold text-brand-700 cursor-pointer"><Lock size={12} /> Manager unlock</button>)}
          </div>
        </Field>
        {(line.track_batches || line.batch_no) && (
          <Field label="Batch number" className="sm:col-span-2"><Input className="font-mono" value={batch} onChange={(e) => setBatch(e.target.value)} /></Field>
        )}
      </div>
      {err && <div className="mt-3 rounded-xl bg-rose-50 px-3 py-2 text-sm text-rose-700">{err}</div>}
    </Modal>
  )
}
