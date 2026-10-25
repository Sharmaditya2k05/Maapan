'use client'

import { useEffect, useState } from 'react'
import { Badge, Card } from '@/components/ui'
import { api, API_URL } from '@/lib/api'
import { MODELS } from '@/lib/models'
import { useSessionUser } from '@/lib/hooks'
import { ROLE_LABEL } from '@/lib/rbac'
import pkg from '../../../package.json'

const ENV = process.env.NEXT_PUBLIC_APP_ENV || 'dev'

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return <div className="flex flex-col gap-0.5 py-2 text-sm sm:flex-row sm:justify-between sm:gap-4"><span className="text-slate-500">{label}</span><span className="min-w-0 break-all font-medium sm:text-right">{children}</span></div>
}

type Health = 'checking' | 'up' | 'down'

/** All fourteen Maapan models with a live health check for each deployed service. */
function ModelsCard() {
  const [health, setHealth] = useState<Record<string, Health>>({})
  useEffect(() => {
    let alive = true
    for (const m of MODELS) {
      if (!m.health) continue
      setHealth((h) => ({ ...h, [m.key]: 'checking' }))
      api<{ status: string }>(m.health)
        .then((r) => alive && setHealth((h) => ({ ...h, [m.key]: r.status === 'ok' ? 'up' : 'down' })))
        .catch(() => alive && setHealth((h) => ({ ...h, [m.key]: 'down' })))
    }
    return () => { alive = false }
  }, [])
  const status = (key: string, kind: string) => {
    if (kind === 'gateway') return <Badge tone="green">serving this app</Badge>
    if (kind === 'app') return <Badge tone="green">running</Badge>
    if (kind === 'library') return <Badge tone="gray">built in</Badge>
    if (kind === 'ops') return <Badge tone="gray">tooling</Badge>
    const h = health[key]
    return h === 'up' ? <Badge tone="green">up</Badge> : h === 'down' ? <Badge tone="amber">waking / down</Badge> : <Badge tone="gray">checking…</Badge>
  }
  return (
    <Card title={`Maapan models (${MODELS.length})`}>
      <div className="divide-y divide-slate-100">
        {MODELS.map((m) => (
          <div key={m.key} className="flex items-start justify-between gap-3 py-2 text-sm">
            <div className="min-w-0"><span className="font-mono font-semibold">{m.key}</span><div className="text-xs text-slate-500">{m.role}</div></div>
            <div className="shrink-0">{status(m.key, m.kind)}</div>
          </div>
        ))}
      </div>
    </Card>
  )
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
    <div className="space-y-4">
    <ModelsCard />
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
    </div>
  )
}
