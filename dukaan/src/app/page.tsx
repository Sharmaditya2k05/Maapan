'use client'

import { useEffect } from 'react'
import { useRouter } from 'next/navigation'
import { session } from '@/lib/session'
import { Spinner } from '@/components/ui'

/** The root sends signed-in users to the app and everyone else to the login screen. */
export default function Home() {
  const router = useRouter()
  useEffect(() => {
    router.replace(session.accessToken() || session.refreshToken() ? '/app' : '/login')
  }, [router])
  return <div className="grid min-h-dvh place-items-center"><Spinner className="h-8 w-8" /></div>
}
