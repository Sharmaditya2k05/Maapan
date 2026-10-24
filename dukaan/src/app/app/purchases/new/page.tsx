'use client'

/**
 * Purchase bill / debit note editor. Also the landing point for drafts other
 * screens hand off through sessionStorage (re-order suggestions, AI bill scan,
 * return-to-vendor): the draft is read once and cleared.
 */
import { useEffect, useMemo, useState } from 'react'
import { useRouter } from 'next/navigation'
import { ShoppingCart } from 'lucide-react'
import { Card, Empty, PageHeader, Skeleton, useToast } from '@/components/ui'
import { useGet, usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { PURCHASE_DRAFT_KEY, type PurchaseDraft } from '@/lib/types.more'
import type { InvoiceFull } from '@/lib/types.pos'
import { lineToCart, parsePurchaseDraft } from '@/lib/pos/invoice'
import { InvoiceEditor, type EditorDraft } from '@/components/invoice/InvoiceEditor'

export default function NewPurchasePage() {
  const router = useRouter()
  const canDo = usePerm()
  const toast = useToast()
  const [handoff, setHandoff] = useState<PurchaseDraft | null | undefined>(undefined)

  useEffect(() => {
    try {
      const d = parsePurchaseDraft(sessionStorage.getItem(PURCHASE_DRAFT_KEY))
      sessionStorage.removeItem(PURCHASE_DRAFT_KEY)
      setHandoff(d)
    } catch {
      setHandoff(null)
    }
  }, [])

  const debit = handoff?.doc_type === 'purchase_return'
  const ref = useGet<InvoiceFull>(debit && handoff?.ref_invoice_id ? `/aadesh/v1/invoices/${handoff.ref_invoice_id}` : null)
  const draft = useMemo<EditorDraft | null>(() => {
    if (!handoff) return null
    return {
      party_id: handoff.party_id || (ref.data?.party_id ?? undefined),
      lines: handoff.lines.map((l) => lineToCart(l, { price_includes_tax: !!l.price_includes_tax })),
      notes: handoff.notes,
      ref_invoice_id: handoff.ref_invoice_id,
      supplier_invoice_no: handoff.supplier_invoice_no,
      supplier_invoice_date: handoff.supplier_invoice_date,
    }
  }, [handoff, ref.data?.party_id])

  if (!canDo(PERMS.purchaseManage)) return <Card><Empty icon={ShoppingCart} title="Not allowed" text="Only store managers, accountants and owners enter purchase bills." /></Card>
  if (handoff === undefined || (debit && ref.isLoading)) return <Skeleton />

  const title = debit ? 'Debit note (return to supplier)' : 'New purchase bill'
  const subtitle = debit
    ? `Goods going back to the supplier${ref.data ? ` against ${ref.data.number}` : ''}; the server checks quantities against the original`
    : handoff ? `${handoff.lines.length} line${handoff.lines.length === 1 ? '' : 's'} pre-filled — pick the supplier and check the prices` : 'Enter the supplier’s bill as printed; stock and input credit follow'
  return (
    <>
      <PageHeader back={() => router.push('/app/purchases')} title={title} subtitle={subtitle} />
      <InvoiceEditor docType={debit ? 'purchase_return' : 'purchase'} draft={draft} draftKey={handoff ? 'handoff' : undefined} refInvoice={ref.data}
        afterSave={(inv) => { toast(`${debit ? 'Debit note' : 'Purchase bill'} ${inv.number} saved`); router.replace(debit ? `/app/returns/${inv.id}` : `/app/purchases/${inv.id}`) }} />
    </>
  )
}
