'use client'

import { useState } from 'react'
import { Receipt } from 'lucide-react'
import { PageHeader, Tabs } from '@/components/ui'
import type { DocType } from '@/lib/types'
import { InvoiceList } from '@/components/invoice/InvoiceList'

export default function ReturnsPage() {
  const [tab, setTab] = useState<Extract<DocType, 'sale_return' | 'purchase_return'>>('sale_return')
  return (
    <>
      <PageHeader title="Returns & Notes" subtitle="Credit notes for goods customers bring back; debit notes for goods returned to suppliers"
        actions={<Tabs value={tab} onChange={setTab} options={[{ value: 'sale_return', label: 'Credit notes' }, { value: 'purchase_return', label: 'Debit notes' }]} />} />
      <InvoiceList key={tab} docType={tab} rowHref={(inv) => `/app/returns/${inv.id}`} partyLabel={tab === 'sale_return' ? 'Customer' : 'Supplier'} icon={Receipt}
        emptyTitle={tab === 'sale_return' ? 'No credit notes yet' : 'No debit notes yet'}
        emptyText={tab === 'sale_return' ? 'Open a sales invoice and choose Return to issue one.' : 'Open a purchase bill and choose Return to supplier.'} />
    </>
  )
}
