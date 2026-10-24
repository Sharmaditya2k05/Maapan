'use client'

import { useEffect, useState } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ApiError } from '@/lib/api'
import { ToastProvider } from '@/components/ui/toast'

/** One QueryClient per browser tab; 401s are handled by the API client, so never retry them. */
function makeClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 20_000,
        refetchOnWindowFocus: false,
        retry: (count, err) => {
          if (err instanceof ApiError && (err.status === 401 || err.status === 403 || err.status === 404 || err.status === 422)) return false
          // Unreachable gateway: one quick retry, then show the error instead of a long skeleton.
          if (err instanceof ApiError && err.isNetwork) return count < 1
          return count < 2
        },
        retryDelay: (attempt) => Math.min(800 * 2 ** attempt, 5000),
      },
      mutations: { retry: 0 },
    },
  })
}

export function Providers({ children }: { children: React.ReactNode }) {
  const [client] = useState(makeClient)
  useEffect(() => {
    // The service worker keeps billing alive offline; dev builds skip it so HMR stays simple.
    if (process.env.NODE_ENV === 'production' && 'serviceWorker' in navigator) {
      navigator.serviceWorker.register('/sw.js').catch(() => {})
    }
  }, [])
  return (
    <QueryClientProvider client={client}>
      <ToastProvider>{children}</ToastProvider>
    </QueryClientProvider>
  )
}
