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
import { BankStatements } from '@/components/ledger/bank/BankStatements'
import { Brs } from '@/components/ledger/bank/Brs'
import { Pdc } from '@/components/ledger/bank/Pdc'

const TABS = ['cashbook', 'bankbook', 'transactions', 'statements', 'brs', 'cheques', 'pdc', 'vouchers'] as const
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
      <PageHeader title="Cash & Bank" subtitle="Cash book, bank book, statement matching and BRS, cheques and every voucher in the books"
        actions={canDo(PERMS.ledgerWrite) ? <Link href="/app/bank/vouchers/new"><Button icon={Plus}>New voucher</Button></Link> : undefined} />
      <div className="no-print mb-4">
        <Tabs value={tab} onChange={setTab} options={[
          { value: 'cashbook', label: 'Cash book' }, { value: 'bankbook', label: 'Bank book' }, { value: 'transactions', label: 'Bank transactions' },
          { value: 'statements', label: 'Statements & matching' }, { value: 'brs', label: 'BRS' },
          { value: 'cheques', label: 'Cheques' }, { value: 'pdc', label: 'PDC' }, { value: 'vouchers', label: 'Vouchers' },
        ]} />
      </div>
      <Card>
        {tab === 'cashbook' && <MoneyBook kind="cash" />}
        {tab === 'bankbook' && <MoneyBook kind="bank" />}
        {tab === 'transactions' && <BankTransactions />}
        {tab === 'statements' && <BankStatements />}
        {tab === 'brs' && <Brs />}
        {tab === 'cheques' && <Cheques />}
        {tab === 'pdc' && <Pdc />}
        {tab === 'vouchers' && <VouchersList onOpen={setVoucher} />}
      </Card>
      <VoucherModal voucherId={voucher} onClose={() => setVoucher(null)} onOpen={setVoucher} />
    </>
  )
}
