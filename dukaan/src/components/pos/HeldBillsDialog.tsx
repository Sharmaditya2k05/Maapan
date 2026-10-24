'use client'

/** Parked bills on this device: resume one or throw it away. */
import { PauseCircle, Trash2 } from 'lucide-react'
import { Button, Empty, IconButton, Modal, Money } from '@/components/ui'
import { fmtDate } from '@/lib/hooks'
import type { HeldBill } from '@/lib/types.pos'

export function HeldBillsDialog({ open, onClose, bills, onResume, onDelete }: {
  open: boolean; onClose: () => void; bills: HeldBill[]; onResume: (b: HeldBill) => void; onDelete: (id: string) => void
}) {
  return (
    <Modal open={open} onClose={onClose} title={`Held bills (${bills.length})`} footer={<Button variant="secondary" onClick={onClose}>Close</Button>}>
      {bills.length === 0 ? (
        <Empty icon={PauseCircle} title="Nothing on hold" text="Press F4 to park the current bill and serve the next customer." />
      ) : (
        <ul className="divide-y divide-slate-100">
          {bills.map((b) => (
            <li key={b.id} className="flex items-center gap-3 py-3">
              <div className="min-w-0 flex-1">
                <div className="text-sm font-semibold">{b.party?.name ?? 'Walk-in'} · {b.lines.length} line{b.lines.length === 1 ? '' : 's'}</div>
                <div className="truncate text-xs text-slate-500">{fmtDate(b.held_at, true)} · {b.lines.slice(0, 3).map((l) => l.name).join(', ')}{b.lines.length > 3 ? '…' : ''}</div>
              </div>
              <Money paise={b.total_paise} className="text-sm font-bold" />
              <Button size="sm" onClick={() => onResume(b)}>Resume</Button>
              <IconButton icon={Trash2} label="Delete held bill" onClick={() => onDelete(b.id)} className="hover:text-rose-600" />
            </li>
          ))}
        </ul>
      )}
    </Modal>
  )
}
