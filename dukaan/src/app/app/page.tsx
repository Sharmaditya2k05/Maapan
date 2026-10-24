'use client'

import Link from 'next/link'
import { AlertTriangle, ArrowRight, IndianRupee, Package, ScanLine, ShoppingCart, TrendingUp, Users, Wallet } from 'lucide-react'
import { Badge, Button, Card, Empty, Money, PageHeader, QueryState, StatCard } from '@/components/ui'
import { useGet, useList, usePerm, useSessionUser, useStore, fmtDate, fmtQty, fyStartIST, todayIST } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import type { Invoice, ListResponse } from '@/lib/types'

interface Summary { sales_paise: number; purchases_paise: number; receivables_paise: number; payables_paise: number; cash_paise: number; bank_paise: number }
interface Suggestion { item_id: string; name: string; unit: string; on_hand: number; reorder_level: number; suggested_qty: number }

export default function DashboardPage() {
  const user = useSessionUser()
  const { storeId, store } = useStore()
  const canDo = usePerm()
  const today = todayIST()
  const month = `${today.slice(0, 7)}-01`

  const summary = useGet<Summary>(canDo(PERMS.reportRead) || canDo(PERMS.ledgerRead) ? '/bahi/v1/reports/summary' : null, { from: month, to: today })
  const fy = useGet<Summary>(canDo(PERMS.reportRead) || canDo(PERMS.ledgerRead) ? '/bahi/v1/reports/summary' : null, { from: fyStartIST(), to: today })
  const recent = useList<Invoice>(canDo(PERMS.invoiceRead) ? '/aadesh/v1/invoices' : null, { doc_type: 'sale', store_id: storeId, limit: 8 })
  const unpaid = useList<Invoice>(canDo(PERMS.invoiceRead) ? '/aadesh/v1/invoices' : null, { doc_type: 'sale', payment_status: 'unpaid', limit: 1 })
  const reorder = useGet<{ items: Suggestion[] }>(canDo(PERMS.stockManage) || canDo(PERMS.purchaseManage) ? '/bhandar/v1/reorder/suggestions' : null, { store_id: storeId })

  const hour = Number(new Date(Date.now() + 5.5 * 3600_000).toISOString().slice(11, 13))
  const greet = hour < 12 ? 'Good morning' : hour < 17 ? 'Good afternoon' : 'Good evening'

  return (
    <>
      <PageHeader
        title={`${greet}${user?.name ? `, ${user.name.split(' ')[0]}` : ''}`}
        subtitle={store ? `${store.name} · ${fmtDate(today)}` : fmtDate(today)}
        actions={
          <>
            {canDo(PERMS.invoiceCreate) && <Link href="/app/pos"><Button icon={ScanLine} variant="gold">Open counter</Button></Link>}
            {canDo(PERMS.purchaseManage) && <Link href="/app/purchases/new"><Button icon={ShoppingCart} variant="secondary">Purchase bill</Button></Link>}
          </>
        }
      />

      <div className="stagger grid grid-cols-2 gap-3 sm:gap-4 xl:grid-cols-4">
        <StatCard label="Sales this month" value={summary.data ? <Money paise={summary.data.sales_paise} /> : '—'} icon={TrendingUp} hint={fy.data ? <>FY <Money paise={fy.data.sales_paise} /></> : undefined} />
        <StatCard label="Purchases this month" value={summary.data ? <Money paise={summary.data.purchases_paise} /> : '—'} icon={ShoppingCart} tone="sky" />
        <StatCard label="To collect" value={summary.data ? <Money paise={summary.data.receivables_paise} /> : '—'} icon={Users} tone="gold" hint={unpaid.data ? `${unpaid.data.total} unpaid bills` : undefined} />
        <StatCard label="Cash + bank" value={summary.data ? <Money paise={summary.data.cash_paise + summary.data.bank_paise} /> : '—'} icon={Wallet} tone="rose" hint={summary.data ? <>to pay <Money paise={summary.data.payables_paise} /></> : undefined} />
      </div>

      <div className="mt-4 grid gap-4 lg:grid-cols-3 sm:mt-6">
        <Card className="lg:col-span-2" title="Recent sales" actions={<Link href="/app/sales" className="link text-xs">All invoices <ArrowRight size={12} className="inline" /></Link>} padded={false}>
          <QueryState isLoading={recent.isLoading} error={recent.error} data={recent.data as ListResponse<Invoice> | undefined} retry={() => recent.refetch()}
            empty={<Empty icon={IndianRupee} title="No sales yet" text="Bills made at the counter show up here." action={canDo(PERMS.invoiceCreate) ? <Link href="/app/pos"><Button icon={ScanLine}>Make the first bill</Button></Link> : undefined} />}>
            {(d) => d.items.length === 0 ? (
              <Empty icon={IndianRupee} title="No sales yet" text="Bills made at the counter show up here." />
            ) : (
              <ul className="divide-y divide-slate-100">
                {d.items.map((inv) => (
                  <li key={inv.id}>
                    <Link href={`/app/sales/${inv.id}`} className="flex items-center gap-3 px-4 py-3 hover:bg-slate-50">
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-sm font-semibold">{inv.party_name}</div>
                        <div className="text-xs text-slate-500">{inv.number} · {fmtDate(inv.invoice_date)}</div>
                      </div>
                      <Badge tone={inv.status === 'cancelled' ? 'red' : inv.payment_status === 'paid' ? 'green' : inv.payment_status === 'partial' ? 'amber' : 'gray'}>
                        {inv.status === 'cancelled' ? 'cancelled' : inv.payment_status}
                      </Badge>
                      <Money paise={inv.grand_total_paise} className="w-28 text-right text-sm font-bold" />
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </QueryState>
        </Card>

        <Card title="Re-order now" actions={<Link href="/app/stock?tab=reorder" className="link text-xs">Stock <ArrowRight size={12} className="inline" /></Link>} padded={false}>
          {!canDo(PERMS.stockManage) && !canDo(PERMS.purchaseManage) ? (
            <Empty icon={Package} title="Stock alerts" text="Store managers see items that need re-ordering here." />
          ) : (
            <QueryState isLoading={reorder.isLoading} error={reorder.error} data={reorder.data?.items} retry={() => reorder.refetch()}
              empty={<Empty icon={Package} title="Stock is healthy" text="Nothing is at or below its re-order level." />}>
              {(items) => (
                <ul className="divide-y divide-slate-100">
                  {items.slice(0, 8).map((s) => (
                    <li key={s.item_id} className="flex items-center gap-3 px-4 py-3">
                      <AlertTriangle size={16} className="shrink-0 text-amber-500" />
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-sm font-semibold">{s.name}</div>
                        <div className="text-xs text-slate-500">{fmtQty(s.on_hand)} {s.unit} left · level {fmtQty(s.reorder_level)}</div>
                      </div>
                      <span className="num text-xs font-semibold text-brand-700">+{fmtQty(s.suggested_qty)}</span>
                    </li>
                  ))}
                </ul>
              )}
            </QueryState>
          )}
        </Card>
      </div>
    </>
  )
}
