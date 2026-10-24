'use client'

import { Suspense } from 'react'
import { useRouter, useSearchParams } from 'next/navigation'
import { ChevronRight, Lock } from 'lucide-react'
import { Empty, PageHeader, Skeleton } from '@/components/ui'
import { usePerm } from '@/lib/hooks'
import { SECTIONS, type SectionId } from '@/components/settings/sections'
import { AboutSection } from '@/components/settings/AboutSection'
import { AiSection } from '@/components/settings/AiSection'
import { BusinessSection } from '@/components/settings/BusinessSection'
import { DeviceSection } from '@/components/settings/DeviceSection'
import { GatewaysSection } from '@/components/settings/GatewaysSection'
import { GstSection } from '@/components/settings/GstSection'
import { NumberingSection } from '@/components/settings/NumberingSection'
import { PeriodLockSection } from '@/components/settings/PeriodLockSection'
import { StoresSection } from '@/components/settings/StoresSection'
import { UsersSection } from '@/components/settings/UsersSection'

const BODY: Record<SectionId, () => React.JSX.Element> = {
  business: BusinessSection, stores: StoresSection, users: UsersSection, numbering: NumberingSection, 'period-lock': PeriodLockSection,
  gateways: GatewaysSection, gst: GstSection, ai: AiSection, device: DeviceSection, about: AboutSection,
}

/** /app/settings?tab=<section>: left section list on desktop, scrollable chips on mobile. */
function SettingsInner() {
  const params = useSearchParams()
  const router = useRouter()
  const canDo = usePerm()
  const visible = SECTIONS.filter((s) => s.perms.length === 0 || s.perms.some(canDo))
  const raw = params.get('tab') ?? params.get('section')
  const requested = SECTIONS.find((s) => s.id === raw)
  const current = visible.find((s) => s.id === raw) ?? visible[0]
  const go = (id: SectionId) => router.replace(`/app/settings?tab=${id}`, { scroll: false })
  const Body = current ? BODY[current.id] : null

  return (
    <>
      <PageHeader title="Settings" subtitle={current ? `${current.label} · ${current.hint}` : undefined} />
      <div className="grid gap-4 lg:grid-cols-[240px_1fr] lg:items-start">
        <nav aria-label="Settings sections" className="-mx-3 flex gap-1 overflow-x-auto px-3 pb-1 [scrollbar-width:none] lg:mx-0 lg:block lg:space-y-1 lg:overflow-visible lg:px-0 lg:pb-0">
          {visible.map((s) => {
            const active = s.id === current?.id
            return (
              <button key={s.id} type="button" onClick={() => go(s.id)} aria-current={active ? 'page' : undefined}
                className={`flex shrink-0 items-center gap-3 rounded-xl px-3 py-2 text-left text-sm transition cursor-pointer lg:w-full ${active ? 'bg-white font-semibold text-brand-700 shadow-sm ring-1 ring-slate-200' : 'text-slate-600 hover:bg-white/70 hover:text-ink'}`}>
                <s.icon size={17} className={active ? 'text-brand-600' : 'text-slate-400'} />
                <span className="min-w-0">
                  <span className="block whitespace-nowrap">{s.label}</span>
                  <span className="hidden text-xs font-normal text-slate-400 lg:block">{s.hint}</span>
                </span>
                {active && <ChevronRight size={14} className="ml-auto hidden text-slate-300 lg:block" />}
              </button>
            )
          })}
        </nav>
        <div className="min-w-0">
          {requested && !visible.includes(requested) && (
            <div className="mb-4 rounded-xl bg-amber-50 px-4 py-2 text-sm text-amber-800">Your role cannot open “{requested.label}”; showing {current?.label} instead.</div>
          )}
          {Body ? <Body /> : <Empty icon={Lock} title="No settings available" text="Your role has nothing to configure here." />}
        </div>
      </div>
    </>
  )
}

export default function SettingsPage() {
  return <Suspense fallback={<Skeleton />}><SettingsInner /></Suspense>
}
