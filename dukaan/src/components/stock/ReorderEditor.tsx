'use client'

import { useEffect, useState } from 'react'
import { Button, ErrorBox, Field, Input, Modal } from '@/components/ui'
import { useSave } from '@/lib/hooks'
import { errorMessage } from '@/lib/api'
import type { ItemFull, ReorderInput } from '@/lib/types.stock'
import { SupplierPicker } from './SupplierPicker'

/** PUT /bhandar/v1/items/{id}/reorder — level, quantity and preferred supplier. */
export function ReorderEditor({ item, onClose, onSaved }: { item: ItemFull | null; onClose: () => void; onSaved?: (it: ItemFull) => void }) {
  const [level, setLevel] = useState('')
  const [qty, setQty] = useState('')
  const [supplier, setSupplier] = useState('')
  const [err, setErr] = useState('')
  useEffect(() => {
    if (!item) return
    setLevel(item.reorder_level ? String(item.reorder_level) : '')
    setQty(item.reorder_qty ? String(item.reorder_qty) : '')
    setSupplier(item.preferred_supplier_id ?? '')
    setErr('')
  }, [item])
  const save = useSave<ItemFull, ReorderInput>(`/bhandar/v1/items/${item?.id ?? ''}/reorder`, {
    method: 'PUT', invalidate: ['/bhandar/v1/items', `/bhandar/v1/items/${item?.id ?? ''}`, '/bhandar/v1/reorder/suggestions'], silent: true,
  })

  const submit = async () => {
    const l = Number(level || 0), q = Number(qty || 0)
    if (!Number.isFinite(l) || !Number.isFinite(q) || l < 0 || q < 0) return setErr('Re-order values cannot be negative.')
    try {
      const it = await save.mutateAsync({ reorder_level: l, reorder_qty: q, preferred_supplier_id: supplier })
      onSaved?.(it)
      onClose()
    } catch (e) { setErr(errorMessage(e)) }
  }

  return (
    <Modal open={!!item} onClose={onClose} title={`Re-order rule · ${item?.name ?? ''}`}
      footer={<><Button variant="secondary" onClick={onClose}>Cancel</Button><Button onClick={submit} loading={save.isPending}>Save rule</Button></>}>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label={`Re-order level (${item?.unit ?? ''})`} hint="Suggest when on-hand is at or below this; 0 switches alerts off">
          <Input value={level} onChange={(e) => setLevel(e.target.value.replace(/[^0-9.]/g, ''))} inputMode="decimal" className="num" />
        </Field>
        <Field label={`Re-order quantity (${item?.unit ?? ''})`} hint="Never less than what brings stock back to the level">
          <Input value={qty} onChange={(e) => setQty(e.target.value.replace(/[^0-9.]/g, ''))} inputMode="decimal" className="num" />
        </Field>
        <Field label="Preferred supplier" className="sm:col-span-2"><SupplierPicker value={supplier} onChange={setSupplier} /></Field>
      </div>
      {err && <div className="mt-4"><ErrorBox message={err} /></div>}
    </Modal>
  )
}
