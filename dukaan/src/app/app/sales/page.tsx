'use client'

import Link from 'next/link'
import { FileText, Plus, ScanLine } from 'lucide-react'
import { Button, PageHeader } from '@/components/ui'
import { usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { InvoiceList } from '@/components/invoice/InvoiceList'

export default function SalesPage() {
  const canDo = usePerm()
  return (
    <>
      <PageHeader title="Sales Invoices" subtitle="Every tax invoice and bill of supply issued at this store"
        actions={canDo(PERMS.invoiceCreate) && (
          <>
            <Link href="/app/pos"><Button variant="secondary" icon={ScanLine}>Counter</Button></Link>
            <Link href="/app/sales/new"><Button icon={Plus}>B2B invoice</Button></Link>
          </>
        )} />
      <InvoiceList docType="sale" rowHref={(inv) => `/app/sales/${inv.id}`} partyLabel="Customer" icon={FileText}
        emptyTitle="No sales yet" emptyText="Bills made at the counter and B2B invoices show up here."
        emptyAction={canDo(PERMS.invoiceCreate) ? <Link href="/app/pos"><Button icon={ScanLine}>Open the counter</Button></Link> : undefined} />
    </>
  )
}
