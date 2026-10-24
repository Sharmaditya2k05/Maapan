'use client'

import { Suspense } from 'react'
import Link from 'next/link'
import { useRouter, useSearchParams } from 'next/navigation'
import { Plus } from 'lucide-react'
import { Button, Card, PageHeader, Skeleton, Tabs } from '@/components/ui'
import { usePerm, useStore } from '@/lib/hooks'
import { PERMS, type Perm } from '@/lib/rbac'
import { StockAdjustments } from '@/components/stock/StockAdjustments'
import { StockAnalytics } from '@/components/stock/StockAnalytics'
import { StockExpiring } from '@/components/stock/StockExpiring'
import { StockOnHand } from '@/components/stock/StockOnHand'
import { StockReorder } from '@/components/stock/StockReorder'
import { StockTransfers } from '@/components/stock/StockTransfers'

type Tab = 'onhand' | 'adjustments' | 'transfers' | 'reorder' | 'expiring' | 'analytics'

/** Tabs and who sees them (any of the perms; empty = everyone who reaches the page). */
const TABS: { value: Tab; label: string; perms: Perm[] }[] = [
  { value: 'onhand', label: 'On hand', perms: [] },
  { value: 'adjustments', label: 'Adjustments', perms: [PERMS.stockManage] },
  { value: 'transfers', label: 'Transfers', perms: [] },
  { value: 'reorder', label: 'Re-order', perms: [PERMS.stockManage, PERMS.purchaseManage] },
  { value: 'expiring', label: 'Expiring', perms: [] },
  { value: 'analytics', label: 'Analytics', perms: [PERMS.reportRead, PERMS.stockManage] },
]

function StockTabs() {
  const params = useSearchParams()
  const router = useRouter()
  const canDo = usePerm()
  const { stores } = useStore()
  const visible = TABS.filter((t) => t.perms.length === 0 || t.perms.some(canDo))
  const raw = params.get('tab')
  const tab: Tab = visible.some((t) => t.value === raw) ? (raw as Tab) : 'onhand'
  const setTab = (t: Tab) => router.replace(`/app/stock${t === 'onhand' ? '' : `?tab=${t}`}`, { scroll: false })

  return (
    <>
      <PageHeader title="Stock & Transfers" subtitle="On hand, adjustments, inter-store transfers, re-order and expiry"
        actions={tab === 'transfers' && canDo(PERMS.stockManage) && stores.length > 1 ? <Link href="/app/stock/transfers/new"><Button icon={Plus}>New transfer</Button></Link> : undefined} />
      <Card padded={false}>
        <div className="border-b border-slate-100 px-3 py-3 sm:px-4">
          <Tabs value={tab} onChange={setTab} options={visible.map(({ value, label }) => ({ value, label }))} />
        </div>
        {tab === 'onhand' && <StockOnHand />}
        {tab === 'adjustments' && <StockAdjustments />}
        {tab === 'transfers' && <StockTransfers />}
        {tab === 'reorder' && <StockReorder />}
        {tab === 'expiring' && <StockExpiring />}
        {tab === 'analytics' && <StockAnalytics />}
      </Card>
    </>
  )
}

export default function StockPage() {
  return <Suspense fallback={<Skeleton />}><StockTabs /></Suspense>
}
