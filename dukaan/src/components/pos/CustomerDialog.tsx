'use client'

/** Attach a saved customer to the bill (search + quick add) or go back to walk-in. */
import { Button, Modal } from '@/components/ui'
import type { Party } from '@/lib/types'
import { PartyPicker } from '@/components/invoice/PartyPicker'

export function CustomerDialog({ open, onClose, value, onChange }: { open: boolean; onClose: () => void; value: Party | null; onChange: (p: Party | null) => void }) {
  return (
    <Modal open={open} onClose={onClose} title="Customer"
      footer={<>{value && <Button variant="ghost" className="mr-auto" onClick={() => { onChange(null); onClose() }}>Bill as walk-in</Button>}<Button variant="secondary" onClick={onClose}>Done</Button></>}>
      <PartyPicker value={value} onChange={(p) => { onChange(p); if (p) onClose() }} type="customer" autoFocus placeholder="Name, mobile or GSTIN" />
      {!value && <p className="mt-3 text-xs text-slate-500">A saved customer enables credit sales, inter-state GST and WhatsApp receipts. Walk-in bills need none of that.</p>}
    </Modal>
  )
}
