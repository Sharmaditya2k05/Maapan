'use client'

/** A line for something not in the item master: name, price, GST %, qty, unit. */
import { useEffect, useState } from 'react'
import { Button, Field, Input, Modal, MoneyInput, Select, Toggle } from '@/components/ui'
import { GST_SLABS_BP } from '@/lib/gst'
import { formatRate } from '@/lib/money'
import type { CartLine } from '@/lib/types.pos'
import { freeTextLine } from '@/lib/pos/cart'

export function FreeTextDialog({ open, onClose, onAdd, presetName }: { open: boolean; onClose: () => void; onAdd: (line: CartLine) => void; presetName?: string }) {
  const [name, setName] = useState('')
  const [price, setPrice] = useState(0)
  const [rate, setRate] = useState(1800)
  const [qty, setQty] = useState('1')
  const [unit, setUnit] = useState('PCS')
  const [hsn, setHsn] = useState('')
  const [incl, setIncl] = useState(true)
  const [err, setErr] = useState<string | null>(null)
  useEffect(() => { if (open) { setName(presetName ?? ''); setPrice(0); setRate(1800); setQty('1'); setUnit('PCS'); setHsn(''); setIncl(true); setErr(null) } }, [open, presetName])

  const submit = () => {
    const q = Number(qty)
    if (!name.trim()) return setErr('Name is required')
    if (!(price > 0)) return setErr('Enter a price')
    if (!(q > 0)) return setErr('Quantity must be more than zero')
    onAdd(freeTextLine({ name, unit_price_paise: price, tax_rate_bp: rate, qty: q, unit, hsn, price_includes_tax: incl }))
    onClose()
  }

  return (
    <Modal open={open} onClose={onClose} title="Free-text line"
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={submit}>Add to bill</Button></>}>
      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Description" className="sm:col-span-2"><Input autoFocus value={name} onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && submit()} /></Field>
        <Field label="Price per unit"><MoneyInput value={price} onChange={setPrice} onKeyDown={(e) => e.key === 'Enter' && submit()} /></Field>
        <Field label="GST rate">
          <Select value={rate} onChange={(e) => setRate(Number(e.target.value))}>{GST_SLABS_BP.map((bp) => <option key={bp} value={bp}>{formatRate(bp)}</option>)}</Select>
        </Field>
        <Field label="Quantity"><Input inputMode="decimal" value={qty} onChange={(e) => setQty(e.target.value.replace(/[^0-9.]/g, ''))} /></Field>
        <Field label="Unit"><Input value={unit} onChange={(e) => setUnit(e.target.value.toUpperCase())} maxLength={12} /></Field>
        <Field label="HSN / SAC (optional)"><Input className="font-mono" inputMode="numeric" value={hsn} onChange={(e) => setHsn(e.target.value.replace(/\D/g, '').slice(0, 8))} /></Field>
        <div className="flex items-end pb-2"><Toggle checked={incl} onChange={setIncl} label="Price includes GST" /></div>
      </div>
      {err && <div className="mt-3 rounded-xl bg-rose-50 px-3 py-2 text-sm text-rose-700">{err}</div>}
    </Modal>
  )
}
