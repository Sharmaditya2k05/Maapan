'use client'

import { useEffect, useState } from 'react'
import { Badge, Card } from '@/components/ui'
import { API_URL } from '@/lib/api'
import { useSessionUser } from '@/lib/hooks'
import { ROLE_LABEL } from '@/lib/rbac'
import pkg from '../../../package.json'

const ENV = process.env.NEXT_PUBLIC_APP_ENV || 'dev'

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return <div className="flex flex-col gap-0.5 py-2 text-sm sm:flex-row sm:justify-between sm:gap-4"><span className="text-slate-500">{label}</span><span className="min-w-0 break-all font-medium sm:text-right">{children}</span></div>
}

export function AboutSection() {
  const user = useSessionUser()
  const [sw, setSw] = useState<'yes' | 'no' | ''>('')
  const [ua, setUa] = useState('')
  useEffect(() => {
    setUa(navigator.userAgent)
    if ('serviceWorker' in navigator) navigator.serviceWorker.getRegistration().then((r) => setSw(r ? 'yes' : 'no')).catch(() => setSw('no'))
    else setSw('no')
  }, [])
  return (
    <Card title="About Maapan">
      <div className="divide-y divide-slate-100">
        <Row label="App">{pkg.name} <span className="font-mono">v{pkg.version}</span> <Badge tone={ENV === 'prod' ? 'green' : ENV === 'staging' ? 'amber' : 'gray'}>{ENV}</Badge></Row>
        <Row label="API gateway"><code className="font-mono text-xs">{API_URL}</code></Row>
        <Row label="Business">{user?.business_name ?? '—'} <span className="font-mono text-xs text-slate-500">{user?.tenant_id.slice(0, 8)}</span></Row>
        <Row label="Signed in as">{user ? `${user.name || user.phone} · ${ROLE_LABEL[user.role]}` : '—'}</Row>
        <Row label="Offline support">{sw === '' ? '…' : sw === 'yes' ? 'service worker active' : 'service worker not registered (dev build or unsupported browser)'}</Row>
        <Row label="Browser"><span className="text-xs text-slate-600">{ua || '…'}</span></Row>
      </div>
      <p className="mt-4 text-xs text-slate-500">Money is stored in paise, tax rates in basis points; totals are computed exactly as the server does. Report problems with the version and API URL above.</p>
    </Card>
  )
}
