'use client'

import { useEffect, useState } from 'react'
import { usePathname, useRouter } from 'next/navigation'
import { AppShell } from '@/components/AppShell'
import { Spinner } from '@/components/ui'
import { session } from '@/lib/session'
import { refreshSessionUser } from '@/lib/pehchaan'
import { useSessionUser } from '@/lib/hooks'

/**
 * Everything under /app needs a session. Tokens live in the browser, so the
 * check runs on the client: no token → /login; token but no cached user
 * (fresh tab) → load it from pehchaan first.
 */
export default function AppLayout({ children }: { children: React.ReactNode }) {
  const router = useRouter()
  const pathname = usePathname()
  const user = useSessionUser()
  const [ready, setReady] = useState(false)

  useEffect(() => {
    let alive = true
    if (!session.accessToken() && !session.refreshToken()) {
      router.replace(`/login?next=${encodeURIComponent(pathname)}`)
      return
    }
    if (session.user()) { setReady(true); return }
    refreshSessionUser()
      .then(() => alive && setReady(true))
      .catch(() => { session.clear(); router.replace('/login') })
    return () => { alive = false }
  }, [router, pathname])

  if (!ready || !user) {
    return <div className="grid min-h-dvh place-items-center"><Spinner className="h-8 w-8" /></div>
  }
  return <AppShell>{children}</AppShell>
}
