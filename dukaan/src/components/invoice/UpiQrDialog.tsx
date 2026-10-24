'use client'

/**
 * Shows a UPI QR for a lenden payment request and polls it every 2 s until it
 * is paid (→ onPaid) or expires. The caller supplies `create` (counter: POST
 * /lenden/v1/payment-requests; invoice page: POST /aadesh/v1/invoices/{id}/payment-request).
 * In the local environment a "Simulate payment" button hits the mock gateway.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import QRCode from 'qrcode'
import { CheckCircle2, Smartphone } from 'lucide-react'
import { Badge, Button, ErrorBox, Modal, Money, Spinner, statusTone } from '@/components/ui'
import { errorMessage, get, post } from '@/lib/api'
import type { PaymentRequestFull } from '@/lib/types.pos'

export const IS_LOCAL = process.env.NEXT_PUBLIC_APP_ENV === 'local'

const TERMINAL = new Set(['paid', 'expired', 'cancelled', 'failed'])

export function UpiQrDialog({ open, onClose, amountPaise, create, onPaid, title = 'Collect by UPI' }: {
  open: boolean
  onClose: () => void
  amountPaise: number
  /** Creates the lenden request. Called once each time the dialog opens. */
  create: () => Promise<PaymentRequestFull>
  onPaid: (req: PaymentRequestFull) => void
  title?: string
}) {
  const [req, setReq] = useState<PaymentRequestFull | null>(null)
  const [qr, setQr] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [simulating, setSimulating] = useState(false)
  const [left, setLeft] = useState<number | null>(null)
  const started = useRef(false)
  const done = useRef(false)
  const createRef = useRef(create)
  const onPaidRef = useRef(onPaid)
  useEffect(() => { createRef.current = create; onPaidRef.current = onPaid }, [create, onPaid])

  // Create the request once per opening.
  useEffect(() => {
    if (!open) {
      started.current = false
      done.current = false
      setReq(null); setQr(null); setError(null); setLeft(null)
      return
    }
    if (started.current) return
    started.current = true
    let alive = true
    setCreating(true)
    createRef.current()
      .then((r) => { if (alive) setReq(r) })
      .catch((e) => { if (alive) setError(errorMessage(e)) })
      .finally(() => { if (alive) setCreating(false) })
    return () => { alive = false }
  }, [open])

  // QR image from the intent (or the gateway's image).
  useEffect(() => {
    if (!req) return
    let alive = true
    if (req.upi_intent) {
      QRCode.toDataURL(req.upi_intent, { width: 320, margin: 1, errorCorrectionLevel: 'M' })
        .then((url) => { if (alive) setQr(url) })
        .catch(() => { if (alive) setQr(req.upi_qr_image_url ?? null) })
    } else setQr(req.upi_qr_image_url ?? null)
    return () => { alive = false }
  }, [req?.id, req?.upi_intent, req?.upi_qr_image_url, req])

  const finish = useCallback((r: PaymentRequestFull) => {
    if (done.current) return
    done.current = true
    onPaidRef.current(r)
  }, [])

  // Poll every 2 s until a terminal status.
  useEffect(() => {
    if (!open || !req || TERMINAL.has(req.status)) return
    const id = req.id
    const t = setInterval(async () => {
      try {
        const r = await get<PaymentRequestFull>(`/lenden/v1/payment-requests/${id}`)
        setReq(r)
        if (r.status === 'paid') finish(r)
        else if (TERMINAL.has(r.status)) setError(`Payment request ${r.status}. Ask the customer to try again.`)
      } catch {
        /* transient: keep polling */
      }
    }, 2000)
    return () => clearInterval(t)
  }, [open, req, finish])

  useEffect(() => {
    if (req?.status === 'paid') finish(req)
  }, [req, finish])

  // Expiry countdown.
  useEffect(() => {
    if (!req?.expires_at || TERMINAL.has(req.status)) { setLeft(null); return }
    const tick = () => setLeft(Math.max(0, Math.floor((new Date(req.expires_at).getTime() - Date.now()) / 1000)))
    tick()
    const t = setInterval(tick, 1000)
    return () => clearInterval(t)
  }, [req?.expires_at, req?.status])

  const simulate = async () => {
    if (!req) return
    setSimulating(true)
    try {
      const r = await post<PaymentRequestFull>(`/lenden/v1/dev/simulate-payment/${req.id}`, {})
      setReq(r)
      if (r.status === 'paid') finish(r)
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setSimulating(false)
    }
  }

  const paid = req?.status === 'paid'
  return (
    <Modal open={open} onClose={onClose} title={title}
      footer={
        <>
          {IS_LOCAL && req && !paid && !TERMINAL.has(req.status) && <Button variant="secondary" onClick={simulate} loading={simulating}>Simulate payment</Button>}
          <Button variant={paid ? 'primary' : 'secondary'} onClick={onClose}>{paid ? 'Done' : 'Cancel'}</Button>
        </>
      }>
      <div className="flex flex-col items-center text-center">
        <div className="text-xs font-semibold uppercase tracking-wide text-slate-500">Amount</div>
        <Money paise={req?.amount_paise ?? amountPaise} className="text-3xl font-extrabold text-ink" />
        {error && <div className="mt-3 w-full"><ErrorBox message={error} /></div>}
        {!error && (
          <div className="mt-4 grid place-items-center">
            {paid ? (
              <div className="grid h-40 w-40 place-items-center rounded-3xl bg-emerald-50 text-emerald-600"><CheckCircle2 size={72} /></div>
            ) : qr ? (
              // eslint-disable-next-line @next/next/no-img-element
              <img src={qr} alt="UPI QR code" width={240} height={240} className="rounded-2xl border border-slate-200 p-2" />
            ) : (
              <div className="grid h-60 w-60 place-items-center rounded-2xl border border-dashed border-slate-300 text-slate-400">{creating ? <Spinner /> : 'No QR available'}</div>
            )}
          </div>
        )}
        <div className="mt-3 flex items-center gap-2 text-sm text-slate-600">
          {req && <Badge tone={statusTone(req.status)}>{req.status}</Badge>}
          {paid ? <span>Payment received{req?.gateway_ref ? ` · Ref ${req.gateway_ref}` : ''}</span>
            : req && !TERMINAL.has(req.status) ? <span className="inline-flex items-center gap-1.5"><Spinner className="h-4 w-4" /> Waiting for the customer to pay{left !== null ? ` · ${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}` : ''}</span>
            : creating ? <span>Creating request…</span> : null}
        </div>
        {req?.upi_intent && !paid && (
          <a href={req.upi_intent} className="mt-3 inline-flex items-center gap-1.5 text-sm font-semibold text-brand-700 sm:hidden"><Smartphone size={14} /> Open in a UPI app</a>
        )}
        {req?.short_url && !paid && <a href={req.short_url} target="_blank" rel="noreferrer" className="link mt-2 text-xs">{req.short_url}</a>}
      </div>
    </Modal>
  )
}
