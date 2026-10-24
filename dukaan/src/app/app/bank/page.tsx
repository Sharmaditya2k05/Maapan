'use client'

import { Suspense } from 'react'
import Link from 'next/link'
import { Plus } from 'lucide-react'
import { Button, Card, PageHeader, Skeleton, Tabs } from '@/components/ui'
import { usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { NoPermission, useQueryParam, useTabParam } from '@/components/ledger/bits'
import { VoucherModal } from '@/components/ledger/VoucherModal'
import { MoneyBook } from '@/components/ledger/bank/MoneyBook'
import { BankTransactions } from '@/components/ledger/bank/BankTransactions'
import { Cheques } from '@/components/ledger/bank/Cheques'
import { VouchersList } from '@/components/ledger/bank/VouchersList'

const TABS = ['cashbook', 'bankbook', 'transactions', 'cheques', 'vouchers'] as const
type Tab = (typeof TABS)[number]

export default function BankPage() {
  return <Suspense fallback={<Skeleton />}><BankInner /></Suspense>
}

function BankInner() {
  const canDo = usePerm()
  const [tab, setTab] = useTabParam<Tab>(TABS, 'cashbook')
  const [voucher, setVoucher] = useQueryParam('voucher')

  if (!canDo(PERMS.ledgerRead)) return <><PageHeader title="Cash & Bank" /><Card><NoPermission what="the ledger" /></Card></>

  return (
    <>
      <PageHeader title="Cash & Bank" subtitle="Cash book, bank book, cheques and every voucher in the books"
        actions={canDo(PERMS.ledgerWrite) ? <Link href="/app/bank/vouchers/new"><Button icon={Plus}>New voucher</Button></Link> : undefined} />
      <div className="no-print mb-4">
        <Tabs value={tab} onChange={setTab} options={[
          { value: 'cashbook', label: 'Cash book' }, { value: 'bankbook', label: 'Bank book' }, { value: 'transactions', label: 'Bank transactions' },
          { value: 'cheques', label: 'Cheques' }, { value: 'vouchers', label: 'Vouchers' },
        ]} />
      </div>
      <Card>
        {tab === 'cashbook' && <MoneyBook kind="cash" />}
        {tab === 'bankbook' && <MoneyBook kind="bank" />}
        {tab === 'transactions' && <BankTransactions />}
        {tab === 'cheques' && <Cheques />}
        {tab === 'vouchers' && <VouchersList onOpen={setVoucher} />}
      </Card>
      <VoucherModal voucherId={voucher} onClose={() => setVoucher(null)} onOpen={setVoucher} />
    </>
  )
}
