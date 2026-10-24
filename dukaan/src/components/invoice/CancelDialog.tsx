'use client'

/** Cancel a posted document with a reason; surfaces the server's 409 (notes or online payments block it). */
import { useEffect, useState } from 'react'
import { Button, ErrorBox, Field, Modal, Textarea } from '@/components/ui'
import { useInvalidate, useSave } from '@/lib/hooks'
import type { InvoiceFull } from '@/lib/types.pos'
import { DOC_SHORT } from '@/lib/pos/invoice'

export function CancelDialog({ invoice, open, onClose, onDone }: { invoice: InvoiceFull; open: boolean; onClose: () => void; onDone?: (inv: InvoiceFull) => void }) {
  const [reason, setReason] = useState('')
  const [err, setErr] = useState<string | null>(null)
  const invalidate = useInvalidate()
  useEffect(() => { if (open) { setReason(''); setErr(null) } }, [open])
  const cancel = useSave<InvoiceFull, { reason: string }>(`/aadesh/v1/invoices/${invoice.id}/cancel`, { silent: true })

  const submit = async () => {
    if (reason.trim().length < 3) return setErr('Give a short reason (it is kept on the document).')
    setErr(null)
    try {
      const inv = await cancel.mutateAsync({ reason: reason.trim() })
      invalidate('/aadesh/v1/invoices', `/aadesh/v1/invoices/${invoice.id}`)
      onDone?.(inv)
      onClose()
    } catch (e) {
      setErr((e as Error).message)
    }
  }

  return (
    <Modal open={open} onClose={onClose} title={`Cancel ${DOC_SHORT[invoice.doc_type].toLowerCase()} ${invoice.number}?`}
      footer={<><Button variant="secondary" onClick={onClose}>Keep it</Button><Button variant="danger" onClick={submit} loading={cancel.isPending}>Cancel document</Button></>}>
      <p className="text-sm text-slate-600">Stock and ledger postings are reversed. The number stays used (GST does not allow re-use). Documents with credit/debit notes or unrefunded online payments cannot be cancelled.</p>
      <Field label="Reason" className="mt-4"><Textarea rows={3} autoFocus value={reason} onChange={(e) => setReason(e.target.value)} placeholder="e.g. billed to the wrong customer" /></Field>
      {err && <div className="mt-3"><ErrorBox message={err} /></div>}
    </Modal>
  )
}
