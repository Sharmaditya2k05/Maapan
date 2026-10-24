'use client'

import Link from 'next/link'
import { Plus, ShoppingCart } from 'lucide-react'
import { Button, PageHeader } from '@/components/ui'
import { usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { InvoiceList } from '@/components/invoice/InvoiceList'

export default function PurchasesPage() {
  const canDo = usePerm()
  return (
    <>
      <PageHeader title="Purchases" subtitle="Supplier bills that bring stock in and raise payables"
        actions={canDo(PERMS.purchaseManage) && <Link href="/app/purchases/new"><Button icon={Plus}>Purchase bill</Button></Link>} />
      <InvoiceList docType="purchase" rowHref={(inv) => `/app/purchases/${inv.id}`} partyLabel="Supplier" icon={ShoppingCart}
        emptyTitle="No purchase bills yet" emptyText="Enter supplier bills to update stock, GST input credit and payables."
        emptyAction={canDo(PERMS.purchaseManage) ? <Link href="/app/purchases/new"><Button icon={Plus}>Enter a bill</Button></Link> : undefined} />
    </>
  )
}
