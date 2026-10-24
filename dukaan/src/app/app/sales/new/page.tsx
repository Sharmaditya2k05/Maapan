'use client'

import { Suspense, useMemo } from 'react'
import { useRouter, useSearchParams } from 'next/navigation'
import { FileText } from 'lucide-react'
import { Card, Empty, PageHeader, Skeleton, useToast } from '@/components/ui'
import { useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { InvoiceFull } from '@/lib/types.pos'
import { draftFromInvoice, InvoiceEditor } from '@/components/invoice/InvoiceEditor'

export default function NewSalePage() {
  return <Suspense fallback={<Skeleton />}><NewSale /></Suspense>
}

function NewSale() {
  const router = useRouter()
  const canDo = usePerm()
  const toast = useToast()
  const dup = useSearchParams().get('dup')
  const src = useGet<InvoiceFull>(dup ? `/aadesh/v1/invoices/${dup}` : null)
  const draft = useMemo(() => (src.data ? draftFromInvoice(src.data) : null), [src.data])

  if (!canDo(PERMS.invoiceCreate)) return <Card><Empty icon={FileText} title="Not allowed" text="Your role cannot create invoices." /></Card>
  return (
    <>
      <PageHeader back={() => router.push('/app/sales')} title="New B2B invoice" subtitle={dup ? (src.data ? `Copied from ${src.data.number} — review before saving` : 'Loading the original…') : 'Tax invoice to a registered or saved customer, with place of supply and payment terms'} />
      {dup && src.isLoading ? <Skeleton /> : (
        <InvoiceEditor docType="sale" draft={draft} draftKey={dup ?? undefined} afterSave={(inv) => { toast(`Invoice ${inv.number} saved`); router.replace(`/app/sales/${inv.id}`) }} />
      )}
    </>
  )
}
