'use client'

/**
 * Cancel a posted document with a reason; surfaces the server's 409 (notes, online payments or a
 * live IRN block it). A live IRN gets a "Cancel the IRN first" button: the IRN is cancelled in kar,
 * then the document cancel is retried once aadesh has heard about it.
 */
import { useEffect, useState } from 'react'
import { Ban } from 'lucide-react'
import { Button, ErrorBox, Field, Modal, Textarea } from '@/components/ui'
import { ApiError, errorMessage, post } from '@/lib/api'
import { useInvalidate } from '@/lib/hooks'
import type { InvoiceFull } from '@/lib/types.pos'
import { DOC_SHORT } from '@/lib/pos/invoice'
import { CancelIrnModal } from '@/components/compliance/actions'

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms))

/** aadesh's 409 for a document whose IRN is still live carries {irn, kar_cancel}. */
const liveIrnOf = (e: unknown): string | null => {
  if (!(e instanceof ApiError) || e.status !== 409) return null
  const d = e.details as { irn?: string } | undefined
  return d?.irn || (/active e-invoice|IRN/i.test(e.message) ? 'live' : null)
}

export function CancelDialog({ invoice, open, onClose, onDone }: { invoice: InvoiceFull; open: boolean; onClose: () => void; onDone?: (inv: InvoiceFull) => void }) {
  const [reason, setReason] = useState('')
  const [err, setErr] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState<string | null>(null)
  const [irnOpen, setIrnOpen] = useState(false)
  const invalidate = useInvalidate()
  useEffect(() => { if (open) { setReason(''); setErr(null); setNote(null) } }, [open])
  const liveIrn = liveIrnOf(err)

  const cancel = async (attempts = 1) => {
    if (reason.trim().length < 3) return setErr(new Error('Give a short reason (it is kept on the document).'))
    setErr(null)
    setBusy(true)
    try {
      for (let i = 0; ; i++) {
        try {
          const inv = await post<InvoiceFull>(`/aadesh/v1/invoices/${invoice.id}/cancel`, { reason: reason.trim() })
          invalidate('/aadesh/v1/invoices', `/aadesh/v1/invoices/${invoice.id}`, '/kar/v1/einvoices')
          setNote(null)
          onDone?.(inv)
          onClose()
          return
        } catch (e) {
          // Right after an IRN cancel, aadesh may not have received kar's event yet: wait and retry.
          if (i + 1 < attempts && liveIrnOf(e)) { await sleep(1500); continue }
          throw e
        }
      }
    } catch (e) {
      setErr(e)
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <Modal open={open && !irnOpen} onClose={onClose} title={`Cancel ${DOC_SHORT[invoice.doc_type].toLowerCase()} ${invoice.number}?`}
        footer={<><Button variant="secondary" onClick={onClose}>Keep it</Button><Button variant="danger" onClick={() => void cancel()} loading={busy}>Cancel document</Button></>}>
        <p className="text-sm text-slate-600">Stock and ledger postings are reversed. The number stays used (GST does not allow re-use). Documents with credit/debit notes, unrefunded online payments or a live e-invoice (IRN) cannot be cancelled.</p>
        <Field label="Reason" className="mt-4"><Textarea rows={3} autoFocus value={reason} onChange={(e) => setReason(e.target.value)} placeholder="e.g. billed to the wrong customer" /></Field>
        {note && <p className="mt-3 rounded-xl bg-sky-50 px-4 py-2 text-sm text-sky-800">{note}</p>}
        {!!err && (
          <div className="mt-3 space-y-2">
            <ErrorBox message={errorMessage(err)} />
            {liveIrn && (
              <div className="rounded-xl border border-amber-200 bg-amber-50 p-3 text-sm text-amber-900">
                <p>This invoice is registered on the IRP. Cancel its IRN first (possible within 24 hours of generation, and only without an active e-way bill); the document is cancelled right after.</p>
                <Button size="sm" variant="secondary" icon={Ban} className="mt-2" onClick={() => setIrnOpen(true)}>Cancel the IRN first</Button>
              </div>
            )}
          </div>
        )}
      </Modal>
      {irnOpen && (
        <CancelIrnModal doc={{ id: invoice.id, number: invoice.number, total_paise: invoice.grand_total_paise, party_name: invoice.party_name, store_state: invoice.store_state }}
          onClose={() => setIrnOpen(false)}
          onDone={() => { setIrnOpen(false); setNote('IRN cancelled. Cancelling the document…'); void cancel(4) }} />
      )}
    </>
  )
}
