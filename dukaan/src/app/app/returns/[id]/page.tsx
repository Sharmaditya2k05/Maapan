'use client'

import { useState } from 'react'
import Link from 'next/link'
import { useParams, useRouter } from 'next/navigation'
import { Ban, Printer, Receipt } from 'lucide-react'
import { Badge, Button, Card, Empty, PageHeader, QueryState, useToast } from '@/components/ui'
import { fmtDate, useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { Party } from '@/lib/types'
import type { InvoiceFull } from '@/lib/types.pos'
import { openPrint } from '@/lib/pos/print'
import { DOC_SHORT } from '@/lib/pos/invoice'
import { InvoiceBadges, InvoiceDetail } from '@/components/invoice/InvoiceDetail'
import { CancelDialog } from '@/components/invoice/CancelDialog'

export default function ReturnDetailPage() {
  const { id } = useParams<{ id: string }>()
  const router = useRouter()
  const canDo = usePerm()
  const toast = useToast()
  const inv = useGet<InvoiceFull>(id ? `/aadesh/v1/invoices/${id}` : null)
  const party = useGet<Party>(inv.data?.party_id ? `/bahi/v1/parties/${inv.data.party_id}` : null)
  const ref = useGet<InvoiceFull>(inv.data?.ref_invoice_id ? `/aadesh/v1/invoices/${inv.data.ref_invoice_id}` : null)
  const [cancelOpen, setCancelOpen] = useState(false)

  return (
    <QueryState isLoading={inv.isLoading} error={inv.error} data={inv.data} retry={() => inv.refetch()}
      empty={<Card><Empty icon={Receipt} title="Note not found" action={<Link href="/app/returns"><Button>Back to returns</Button></Link>} /></Card>}>
      {(doc) => (
        <>
          <PageHeader back={() => router.push('/app/returns')}
            title={<span className="flex flex-wrap items-center gap-2"><span className="font-mono">{doc.number}</span><Badge tone="brand">{DOC_SHORT[doc.doc_type]}</Badge><InvoiceBadges inv={doc} /></span>}
            subtitle={<>{doc.party_name} · {fmtDate(doc.invoice_date)}{ref.data ? <> · against <Link href={ref.data.doc_type === 'purchase' ? `/app/purchases/${ref.data.id}` : `/app/sales/${ref.data.id}`} className="link font-mono">{ref.data.number}</Link></> : null}</>}
            actions={
              <>
                <Button variant="secondary" icon={Printer} onClick={() => openPrint(doc.id, 'a4')}>Print</Button>
                {doc.status === 'posted' && canDo(PERMS.invoiceCancel) && <Button variant="ghost" icon={Ban} className="text-rose-600 hover:bg-rose-50 hover:text-rose-700" onClick={() => setCancelOpen(true)}>Cancel</Button>}
              </>
            } />
          {doc.status === 'cancelled' && <div className="mb-4 rounded-xl bg-rose-50 px-4 py-3 text-sm text-rose-700"><Badge tone="red">Cancelled</Badge> {doc.cancel_reason}</div>}
          <InvoiceDetail invoice={doc} party={party.data} refInvoice={ref.data} />
          <CancelDialog invoice={doc} open={cancelOpen} onClose={() => setCancelOpen(false)} onDone={() => toast(`${doc.number} cancelled`)} />
        </>
      )}
    </QueryState>
  )
}
