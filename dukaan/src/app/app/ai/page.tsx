'use client'

import { Suspense } from 'react'
import Link from 'next/link'
import { useRouter, useSearchParams } from 'next/navigation'
import { Sparkles } from 'lucide-react'
import { Badge, Empty, PageHeader, Tabs } from '@/components/ui'
import { usePerm } from '@/lib/hooks'
import { PERMS } from '@/lib/rbac'
import { AiUnavailable, useAiStatus } from '@/components/ai/common'
import { DashboardTab } from '@/components/ai/DashboardTab'
import { AskTab } from '@/components/ai/AskTab'
import { ScanTab } from '@/components/ai/ScanTab'
import { InsightsTab } from '@/components/ai/InsightsTab'
import { ForecastTab } from '@/components/ai/ForecastTab'

type Tab = 'dashboard' | 'ask' | 'scan' | 'insights' | 'forecast'
const TABS: { value: Tab; label: string; perm: 'ai' | 'report' }[] = [
  { value: 'dashboard', label: 'Dashboard', perm: 'report' },
  { value: 'ask', label: 'Ask Taraju', perm: 'ai' },
  { value: 'scan', label: 'Scan a bill', perm: 'ai' },
  { value: 'insights', label: 'Insights', perm: 'ai' },
  { value: 'forecast', label: 'Forecast', perm: 'ai' },
]

export default function AiPage() {
  return <Suspense><AiInner /></Suspense>
}

function AiInner() {
  const canDo = usePerm()
  const router = useRouter()
  const params = useSearchParams()
  const ai = canDo(PERMS.aiUse)
  const report = canDo(PERMS.reportRead)
  const tabs = TABS.filter((t) => (t.perm === 'ai' ? ai : report))
  const requested = params.get('tab') as Tab | null
  const tab: Tab = tabs.some((t) => t.value === requested) ? (requested as Tab) : (tabs[0]?.value ?? 'dashboard')
  const status = useAiStatus(ai)

  if (tabs.length === 0) return <Empty icon={Sparkles} title="Ask Taraju" text="Owners, store managers and accountants can use the AI assistant and insights." />

  const usesLLM = tab === 'ask' || tab === 'scan'
  const budget = status.data
  const pct = budget && budget.token_budget > 0 ? Math.min(100, Math.round((100 * budget.tokens_used) / budget.token_budget)) : null

  return (
    <>
      <PageHeader title="Ask Taraju" subtitle="Live dashboard, answers from your books, bill scanning, anomaly checks and re-order forecasts"
        actions={budget && ai && (
          <Link href="/app/settings?tab=ai" className="flex items-center gap-2 text-xs text-slate-500" title="AI tokens used this month">
            {pct !== null && <span className="h-2 w-24 overflow-hidden rounded-full bg-slate-200"><span className={`block h-full ${pct >= 90 ? 'bg-rose-500' : 'bg-brand-500'}`} style={{ width: `${pct}%` }} /></span>}
            <span className="num">{budget.tokens_used.toLocaleString('en-IN')}{budget.token_budget > 0 ? ` / ${budget.token_budget.toLocaleString('en-IN')}` : ''} tokens</span>
            {!budget.ai_enabled && <Badge tone="amber">AI off</Badge>}
            {!budget.ai_configured && <Badge tone="red">not configured</Badge>}
          </Link>
        )} />
      <div className="mb-4"><Tabs value={tab} onChange={(t) => router.replace(`/app/ai?tab=${t}`)} options={tabs.map(({ value, label }) => ({ value, label }))} /></div>

      {usesLLM && budget && !budget.ai_configured ? (
        <AiUnavailable message="The server has no AI key configured, so Ask Taraju and bill scanning answer 503. Dashboard, insights and forecasts still work." />
      ) : (
        <>
          {usesLLM && budget && !budget.ai_enabled && (
            <div className="mb-4 rounded-xl border border-amber-200 bg-amber-50 px-4 py-2.5 text-sm text-amber-900">The AI pilot flag is off for this business. If requests are refused, an owner can turn it on in <Link href="/app/settings?tab=ai" className="link">Settings → AI</Link>.</div>
          )}
          {usesLLM && pct !== null && pct >= 100 && (
            <div className="mb-4 rounded-xl border border-rose-200 bg-rose-50 px-4 py-2.5 text-sm text-rose-800">This month&apos;s AI token budget is used up; it resets on the 1st. Raise the budget in Settings → AI.</div>
          )}
          {tab === 'dashboard' && <DashboardTab />}
          {tab === 'ask' && <AskTab />}
          {tab === 'scan' && <ScanTab />}
          {tab === 'insights' && <InsightsTab />}
          {tab === 'forecast' && <ForecastTab />}
        </>
      )}
    </>
  )
}
