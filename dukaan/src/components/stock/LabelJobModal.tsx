'use client'

import { useEffect, useState } from 'react'
import { useRouter } from 'next/navigation'
import { Printer } from 'lucide-react'
import { Badge, Button, Field, Input, Modal, Select, Toggle } from '@/components/ui'
import { storeLabelJob } from '@/lib/stock/handoff'
import type { ItemFull } from '@/lib/types.stock'

type LabelItem = Pick<ItemFull, 'id' | 'name' | 'barcode'>

/** Copies per item, columns per sheet and price toggle, then off to the print view. */
export function LabelJobModal({ open, onClose, items }: { open: boolean; onClose: () => void; items: LabelItem[] }) {
  const router = useRouter()
  const [copies, setCopies] = useState<Record<string, string>>({})
  const [columns, setColumns] = useState(4)
  const [showPrice, setShowPrice] = useState(true)
  useEffect(() => { if (open) setCopies({}) }, [open])
  const printable = items.filter((i) => i.barcode)
  const skipped = items.length - printable.length

  const go = () => {
    storeLabelJob({
      items: printable.map((i) => ({ item_id: i.id, copies: Math.min(500, Math.max(1, Number(copies[i.id] ?? 1) || 1)) })),
      show_price: showPrice, columns,
    })
    onClose()
    router.push('/app/items/labels')
  }

  return (
    <Modal open={open} onClose={onClose} title={`Print labels · ${items.length} item${items.length === 1 ? '' : 's'}`}
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button icon={Printer} onClick={go} disabled={printable.length === 0}>Open print view</Button></>}>
      <div className="mb-4 grid gap-4 sm:grid-cols-2">
        <Field label="Columns per sheet">
          <Select value={columns} onChange={(e) => setColumns(Number(e.target.value))}>{[2, 3, 4, 5, 6].map((n) => <option key={n} value={n}>{n}</option>)}</Select>
        </Field>
        <div className="flex items-end pb-2"><Toggle checked={showPrice} onChange={setShowPrice} label="Show MRP on the sticker" /></div>
      </div>
      {skipped > 0 && <p className="mb-3 text-xs text-amber-700">{skipped} item{skipped === 1 ? ' has' : 's have'} no barcode and will be skipped. Edit the item to add or generate one.</p>}
      <ul className="max-h-72 divide-y divide-slate-100 overflow-y-auto rounded-xl border border-slate-100">
        {items.map((i) => (
          <li key={i.id} className="flex items-center gap-3 px-3 py-2 text-sm">
            <div className="min-w-0 flex-1">
              <div className="truncate font-semibold">{i.name}</div>
              <div className="font-mono text-xs text-slate-500">{i.barcode || <Badge tone="amber">no barcode</Badge>}</div>
            </div>
            <label className="flex items-center gap-2 text-xs text-slate-500">Copies
              <Input type="number" min={1} max={500} value={copies[i.id] ?? '1'} onChange={(e) => setCopies((c) => ({ ...c, [i.id]: e.target.value }))} className="w-20 input-sm num" disabled={!i.barcode} aria-label={`Copies of ${i.name}`} />
            </label>
          </li>
        ))}
      </ul>
    </Modal>
  )
}
