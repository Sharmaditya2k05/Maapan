'use client'

import { createContext, useCallback, useContext, useState, type ReactNode } from 'react'
import { CheckCircle2, Info, X } from 'lucide-react'

type Tone = 'success' | 'error' | 'info'
type ToastItem = { id: number; message: string; tone: Tone }

const ToastCtx = createContext<(message: string, tone?: Tone) => void>(() => {})

/** `const toast = useToast(); toast('Saved'); toast(errorMessage(e), 'error')` */
export const useToast = () => useContext(ToastCtx)

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<ToastItem[]>([])
  const push = useCallback((message: string, tone: Tone = 'success') => {
    const id = Date.now() + Math.random()
    setItems((x) => [...x, { id, message, tone }])
    setTimeout(() => setItems((x) => x.filter((t) => t.id !== id)), tone === 'error' ? 5000 : 2800)
  }, [])
  return (
    <ToastCtx.Provider value={push}>
      {children}
      <div className="no-print pointer-events-none fixed inset-x-0 bottom-20 z-[70] flex flex-col items-center gap-2 px-4 md:bottom-6" aria-live="polite">
        {items.map((t) => (
          <div key={t.id} className="animate-rise pointer-events-auto flex max-w-md items-center gap-2.5 rounded-2xl bg-ink/95 px-4 py-3 text-sm font-medium text-white shadow-2xl shadow-ink/30 ring-1 ring-white/10 backdrop-blur">
            {t.tone === 'success' ? <CheckCircle2 size={18} className="shrink-0 text-emerald-400" /> : t.tone === 'error' ? <X size={18} className="shrink-0 text-rose-400" /> : <Info size={18} className="shrink-0 text-sky-300" />}
            <span>{t.message}</span>
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  )
}
