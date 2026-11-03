'use client'

import { Suspense } from 'react'
import { Card, PageHeader, Skeleton, Tabs } from '@/components/ui'
import { usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { NoPermission, useTabParam } from '@/components/ledger/bits'
import { Tcs1H, Tds26Q, TdsChallans, TdsDeduct, TdsRegister, TdsSettingsCard } from '@/components/ledger/tds/TdsTabs'

const TABS = ['register', 'deduct', 'challans', '26q', 'tcs', 'settings'] as const
type Tab = (typeof TABS)[number]

export default function TdsPage() {
  return <Suspense fallback={<Skeleton />}><TdsInner /></Suspense>
}

function TdsInner() {
  const canDo = usePerm()
  const [tab, setTab] = useTabParam<Tab>(TABS, 'register')
  if (!canDo(PERMS.ledgerRead) && !canDo(PERMS.reportRead)) return <><PageHeader title="TDS / TCS" /><Card><NoPermission what="TDS / TCS" /></Card></>
  const reports = canDo(PERMS.reportRead)
  return (
    <>
      <PageHeader title="TDS / TCS" subtitle="194Q · 194C · 194J deductions, challans, 26Q and the 206C(1H) tracker" />
      <div className="no-print mb-4">
        <Tabs value={tab} onChange={setTab} options={[
          { value: 'register', label: 'Register' }, { value: 'deduct', label: 'Compute & deduct' }, { value: 'challans', label: 'Challans' },
          ...(reports ? [{ value: '26q' as Tab, label: '26Q' }, { value: 'tcs' as Tab, label: '206C(1H)' }] : []),
          { value: 'settings', label: 'Settings' },
        ]} />
      </div>
      {!canDo(PERMS.ledgerRead) && tab !== '26q' && tab !== 'tcs' ? <Card><NoPermission what="the TDS register" /></Card> : <>
        {tab === 'register' && <TdsRegister />}
        {tab === 'deduct' && <TdsDeduct />}
        {tab === 'challans' && <TdsChallans />}
        {tab === 'settings' && <TdsSettingsCard />}
      </>}
      {tab === '26q' && reports && <Tds26Q />}
      {tab === 'tcs' && reports && <Tcs1H />}
    </>
  )
}
