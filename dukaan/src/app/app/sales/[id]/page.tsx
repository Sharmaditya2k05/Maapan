'use client'

import { useCallback, useState } from 'react'
import Link from 'next/link'
import { useParams, useRouter } from 'next/navigation'
import { Ban, Copy, FileText, MessageCircle, Printer, QrCode, Receipt, Undo2 } from 'lucide-react'
import { Badge, Button, Card, Empty, PageHeader, QueryState, useToast } from '@/components/ui'
import { fmtDate, useGet, useInvalidate, usePerm, useSessionUser } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { usePrefs } from '@/lib/prefs'
import { post } from '@/lib/api'
import type { Party } from '@/lib/types'
import type { InvoiceFull, PaymentRequestFull } from '@/lib/types.pos'
import { openPrint, thermalWidth } from '@/lib/pos/print'
import { whatsappLink } from '@/lib/pos/invoice'
import { InvoiceBadges, InvoiceDetail } from '@/components/invoice/InvoiceDetail'
import { CancelDialog } from '@/components/invoice/CancelDialog'
import { UpiQrDialog } from '@/components/invoice/UpiQrDialog'

export default function SaleDetailPage() {
  const { id } = useParams<{ id: string }>()
  const router = useRouter()
  const canDo = usePerm()
  const user = useSessionUser()
  const [prefs] = usePrefs()
  const toast = useToast()
  const invalidate = useInvalidate()
  const inv = useGet<InvoiceFull>(id ? `/aadesh/v1/invoices/${id}` : null)
  const party = useGet<Party>(inv.data?.party_id ? `/bahi/v1/parties/${inv.data.party_id}` : null)
  const [cancelOpen, setCancelOpen] = useState(false)
  const [qrOpen, setQrOpen] = useState(false)

  const createRequest = useCallback(() => post<PaymentRequestFull>(`/aadesh/v1/invoices/${id}/payment-request`, { kind: 'upi_qr' }), [id])

  return (
    <QueryState isLoading={inv.isLoading} error={inv.error} data={inv.data} retry={() => inv.refetch()}
      empty={<Card><Empty icon={FileText} title="Invoice not found" action={<Link href="/app/sales"><Button>Back to sales</Button></Link>} /></Card>}>
      {(doc) => {
        const posted = doc.status === 'posted'
        return (
          <>
            <PageHeader back={() => router.push('/app/sales')}
              title={<span className="flex flex-wrap items-center gap-2"><span className="font-mono">{doc.number}</span><InvoiceBadges inv={doc} /></span>}
              subtitle={<>{doc.party_name} · {fmtDate(doc.invoice_date)}{doc.client_uuid?.startsWith('pos') || doc.created_at !== doc.invoice_date ? '' : ''}</>}
              actions={
                <>
                  {posted && doc.balance_paise > 0 && canDo(PERMS.paymentCollect) && <Button variant="gold" icon={QrCode} onClick={() => setQrOpen(true)}>Request UPI</Button>}
                  <Button variant="secondary" icon={Printer} onClick={() => openPrint(doc.id, 'a4')}>Print A4</Button>
                  <Button variant="secondary" icon={Receipt} onClick={() => openPrint(doc.id, 'thermal', thermalWidth(prefs))}>Receipt</Button>
                  <a href={whatsappLink(doc, user?.business_name ?? '')} target="_blank" rel="noreferrer"><Button variant="secondary" icon={MessageCircle} title={doc.party_phone ? `Send to ${doc.party_phone}` : 'No phone on the bill — pick the contact in WhatsApp'}>WhatsApp</Button></a>
                  {posted && canDo(PERMS.invoiceCreate) && <Link href={`/app/returns/new?ref=${doc.id}`}><Button variant="secondary" icon={Undo2}>Return</Button></Link>}
                  {canDo(PERMS.invoiceCreate) && <Link href={`/app/sales/new?dup=${doc.id}`}><Button variant="ghost" icon={Copy}>Duplicate</Button></Link>}
                  {posted && canDo(PERMS.invoiceCancel) && <Button variant="ghost" icon={Ban} className="text-rose-600 hover:bg-rose-50 hover:text-rose-700" onClick={() => setCancelOpen(true)}>Cancel</Button>}
                </>
              } />
            {doc.status === 'cancelled' && <div className="mb-4 rounded-xl bg-rose-50 px-4 py-3 text-sm text-rose-700"><Badge tone="red">Cancelled</Badge> {doc.cancel_reason}{doc.cancelled_at ? ` · ${fmtDate(doc.cancelled_at, true)}` : ''}</div>}
            <InvoiceDetail invoice={doc} party={party.data} />
            <CancelDialog invoice={doc} open={cancelOpen} onClose={() => setCancelOpen(false)} onDone={() => toast(`${doc.number} cancelled`)} />
            <UpiQrDialog open={qrOpen} onClose={() => { setQrOpen(false); invalidate(`/aadesh/v1/invoices/${id}`) }} amountPaise={doc.balance_paise} create={createRequest} title={`Collect balance on ${doc.number}`}
              onPaid={() => { toast('Payment received'); setTimeout(() => invalidate(`/aadesh/v1/invoices/${id}`, '/aadesh/v1/invoices'), 1500) }} />
          </>
        )
      }}
    </QueryState>
  )
}
