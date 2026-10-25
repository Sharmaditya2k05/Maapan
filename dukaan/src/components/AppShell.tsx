'use client'

import { useEffect, useRef, useState, type ReactNode } from 'react'
import Link from 'next/link'
import { usePathname, useRouter } from 'next/navigation'
import { ChevronDown, CloudOff, LogOut, Menu, RefreshCw, Store as StoreIcon, X } from 'lucide-react'
import { Logo, LogoMark } from './Logo'
import { NAV, NAV_GROUPS } from './nav'
import { Avatar, Badge } from './ui'
import { useOnline, usePerm, useSessionUser, useStore } from '@/lib/hooks'
import { session } from '@/lib/session'
import { post } from '@/lib/api'
import { listQueue, onQueueChange, startAutoSync, syncQueue } from '@/lib/offline'
import { ROLE_LABEL } from '@/lib/rbac'

function Sidebar({ onNavigate, rail = false }: { onNavigate?: () => void; rail?: boolean }) {
  const pathname = usePathname()
  const canDo = usePerm()
  const user = useSessionUser()
  const items = NAV.filter((n) => n.perms.length === 0 || n.perms.some(canDo))
  return (
    <div className="relative flex h-full flex-col overflow-hidden bg-brand-950 text-brand-100">
      <div className="pointer-events-none absolute -left-24 top-1/3 h-64 w-64 rounded-full bg-brand-500/15 blur-3xl" />
      <div className={`relative ${rail ? 'flex justify-center py-5' : 'px-5 pb-4 pt-6'}`}>
        {rail ? <LogoMark size={38} /> : <Logo light tagline size={36} />}
      </div>
      <nav className={`relative flex-1 overflow-y-auto ${rail ? 'space-y-1 px-2.5' : 'px-3'}`} aria-label="Main">
        {NAV_GROUPS.map((g) => {
          const group = items.filter((n) => n.group === g)
          if (!group.length) return null
          return (
            <div key={g} className={rail ? 'space-y-1 border-t border-white/5 pt-2 first:border-0 first:pt-0' : 'mb-4'}>
              {!rail && <div className="px-3 pb-1.5 text-[10px] font-bold uppercase tracking-[0.18em] text-slate-400/70">{g}</div>}
              {group.map(({ href, label, icon: Icon, exact }) => {
                const active = exact ? pathname === href : pathname === href || pathname.startsWith(href + '/')
                return (
                  <Link key={href} href={href} onClick={onNavigate} title={rail ? label : undefined} aria-current={active ? 'page' : undefined}
                    className={`group relative flex items-center rounded-xl text-sm font-medium transition ${rail ? 'h-11 justify-center' : 'gap-3 px-3 py-2.5'} ${
                      active ? 'bg-gradient-to-r from-white/15 to-white/5 text-white shadow-inner ring-1 ring-white/10' : 'text-slate-300/80 hover:bg-white/5 hover:text-white'}`}>
                    {active && <span className="absolute -left-3 top-1/2 h-6 w-1 -translate-y-1/2 rounded-r-full bg-brand-400" />}
                    <Icon size={rail ? 20 : 18} className={active ? 'text-brand-400' : 'transition group-hover:scale-110'} />
                    {!rail && label}
                  </Link>
                )
              })}
            </div>
          )
        })}
      </nav>
      {!rail && user && (
        <div className="relative m-3 rounded-2xl bg-white/5 p-3 ring-1 ring-white/10">
          <div className="truncate text-xs font-semibold text-white">{user.business_name || 'Your business'}</div>
          <div className="mt-0.5 truncate text-[11px] text-brand-200/80">{ROLE_LABEL[user.role]} · {user.phone}</div>
        </div>
      )}
    </div>
  )
}

/** Offline / pending-sync pill in the top bar. */
function SyncPill() {
  const online = useOnline()
  const [pending, setPending] = useState(0)
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    let alive = true
    const refresh = () => { void listQueue().then((q) => alive && setPending(q.filter((b) => b.status !== 'synced').length)) }
    refresh()
    const off = onQueueChange(refresh)
    return () => { alive = false; off() }
  }, [])
  if (online && pending === 0) return null
  const sync = async () => { setBusy(true); try { await syncQueue() } finally { setBusy(false) } }
  return (
    <button type="button" onClick={sync} disabled={!online || busy} title={online ? 'Upload queued bills now' : 'Bills are saved on this device and will upload when you are back online'}
      className={`inline-flex h-9 items-center gap-2 rounded-xl px-3 text-xs font-semibold ring-1 ring-inset transition cursor-pointer disabled:cursor-default ${online ? 'bg-amber-50 text-amber-700 ring-amber-600/20 hover:bg-amber-100' : 'bg-rose-50 text-rose-700 ring-rose-600/15'}`}>
      {online ? <RefreshCw size={14} className={busy ? 'animate-spin' : ''} /> : <CloudOff size={14} />}
      {online ? `${pending} to sync` : pending ? `Offline · ${pending} queued` : 'Offline'}
    </button>
  )
}

function StoreSwitcher() {
  const { store, stores, setStore } = useStore()
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const h = (e: MouseEvent) => { if (!ref.current?.contains(e.target as Node)) setOpen(false) }
    window.addEventListener('mousedown', h)
    return () => window.removeEventListener('mousedown', h)
  }, [])
  if (!store) return null
  return (
    <div ref={ref} className="relative">
      <button type="button" onClick={() => stores.length > 1 && setOpen((o) => !o)}
        className={`inline-flex h-9 max-w-[220px] items-center gap-2 rounded-xl border border-slate-200 bg-white px-3 text-sm font-semibold ${stores.length > 1 ? 'hover:bg-slate-50 cursor-pointer' : 'cursor-default'}`}>
        <StoreIcon size={15} className="shrink-0 text-brand-600" />
        <span className="truncate">{store.name}</span>
        {stores.length > 1 && <ChevronDown size={14} className="shrink-0 text-slate-400" />}
      </button>
      {open && (
        <div className="absolute right-0 z-40 mt-1 w-64 overflow-hidden rounded-2xl border border-slate-200 bg-white py-1 shadow-xl">
          {stores.map((s) => (
            <button key={s.id} type="button" onClick={() => { setStore(s.id); setOpen(false) }}
              className={`flex w-full items-center justify-between px-4 py-2.5 text-left text-sm hover:bg-slate-50 cursor-pointer ${s.id === store.id ? 'font-bold text-brand-700' : ''}`}>
              <span className="truncate">{s.name}</span><span className="ml-2 text-xs text-slate-400">{s.code}</span>
            </button>
          ))}
        </div>
      )}
    </div>
  )
}

function UserMenu() {
  const user = useSessionUser()
  const router = useRouter()
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const h = (e: MouseEvent) => { if (!ref.current?.contains(e.target as Node)) setOpen(false) }
    window.addEventListener('mousedown', h)
    return () => window.removeEventListener('mousedown', h)
  }, [])
  if (!user) return null
  const logout = async () => {
    try { await post('/pehchaan/v1/auth/logout', { refresh_token: session.refreshToken() }) } catch { /* local logout regardless */ }
    session.clear()
    router.replace('/login')
  }
  return (
    <div ref={ref} className="relative">
      <button type="button" onClick={() => setOpen((o) => !o)} className="flex items-center gap-2 rounded-xl p-1 hover:bg-slate-100 cursor-pointer" aria-haspopup="menu">
        <Avatar name={user.name || user.phone} className="h-9 w-9 text-xs" />
      </button>
      {open && (
        <div className="absolute right-0 z-40 mt-1 w-60 overflow-hidden rounded-2xl border border-slate-200 bg-white py-1 shadow-xl" role="menu">
          <div className="px-4 py-3">
            <div className="truncate text-sm font-bold">{user.name || user.phone}</div>
            <div className="mt-0.5 text-xs text-slate-500">{ROLE_LABEL[user.role]}</div>
          </div>
          <div className="border-t border-slate-100" />
          <button type="button" onClick={logout} className="flex w-full items-center gap-2 px-4 py-2.5 text-left text-sm text-rose-600 hover:bg-rose-50 cursor-pointer" role="menuitem">
            <LogOut size={15} /> Sign out
          </button>
        </div>
      )}
    </div>
  )
}

/** Sidebar + top bar around every authenticated screen. */
export function AppShell({ children }: { children: ReactNode }) {
  const [drawer, setDrawer] = useState(false)
  const pathname = usePathname()
  useEffect(() => setDrawer(false), [pathname])
  // Bills queued offline upload from any screen, not only while the counter is open.
  useEffect(() => startAutoSync(), [])
  const user = useSessionUser()
  const title = NAV.find((n) => (n.exact ? pathname === n.href : pathname === n.href || pathname.startsWith(n.href + '/')))?.label ?? 'Maapan'
  return (
    <div className="flex min-h-dvh">
      <aside className="app-sidebar hidden w-64 shrink-0 lg:block xl:w-[272px]"><div className="sticky top-0 h-dvh"><Sidebar /></div></aside>
      <aside className="app-sidebar hidden w-[72px] shrink-0 md:block lg:hidden"><div className="sticky top-0 h-dvh"><Sidebar rail /></div></aside>
      {drawer && (
        <div className="fixed inset-0 z-50 md:hidden" role="dialog" aria-modal>
          <div className="absolute inset-0 bg-ink/50 backdrop-blur-sm" onClick={() => setDrawer(false)} />
          <div className="absolute inset-y-0 left-0 w-[84%] max-w-xs animate-rise"><Sidebar onNavigate={() => setDrawer(false)} /></div>
          <button type="button" onClick={() => setDrawer(false)} className="absolute right-3 top-3 grid h-10 w-10 place-items-center rounded-xl bg-white/90 text-ink shadow cursor-pointer" aria-label="Close menu"><X size={18} /></button>
        </div>
      )}
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="app-topbar sticky top-0 z-30 flex h-14 items-center gap-2 border-b border-slate-200/80 bg-white/85 px-3 backdrop-blur sm:gap-3 sm:px-5">
          <button type="button" onClick={() => setDrawer(true)} className="grid h-9 w-9 place-items-center rounded-xl text-slate-600 hover:bg-slate-100 md:hidden cursor-pointer" aria-label="Open menu"><Menu size={20} /></button>
          <div className="min-w-0 flex-1 truncate text-sm font-bold sm:text-base">{title}</div>
          <SyncPill />
          <StoreSwitcher />
          {user && <Badge tone="brand" className="hidden sm:inline-flex">{ROLE_LABEL[user.role]}</Badge>}
          <UserMenu />
        </header>
        <main className="flex-1 px-3 py-4 sm:px-5 sm:py-6 lg:px-8">{children}</main>
      </div>
    </div>
  )
}
