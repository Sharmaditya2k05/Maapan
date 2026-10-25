'use client'

/** Ask Maapan: POST /buddhi/v1/ask {question} → {answer, usage}. History lives in component state. */
import { useEffect, useRef, useState } from 'react'
import { Bot, Loader2, RotateCcw, Send, Sparkles, User } from 'lucide-react'
import { Button, Card, Empty, IconButton, Textarea } from '@/components/ui'
import { errorMessage, post } from '@/lib/api'
import { useSessionUser } from '@/lib/hooks'
import type { AiUsage, AskResponse } from '@/lib/types.more'
import { AiUnavailable, UsageChip, isAiUnavailable } from './common'

interface Msg { id: number; role: 'user' | 'assistant'; text: string; usage?: AiUsage; error?: boolean; pending?: boolean }

const SUGGESTIONS = [
  'Is mahine kitna udhaar baaki hai?',
  'Pichhle hafte sabse zyada kya bika?',
  'Which items are running low on stock?',
  'Aaj ki sale kitni hui, kal se zyada ya kam?',
  'Top 5 customers by sales this month',
  'Kis supplier ko sabse zyada payment baaki hai?',
]

let seq = 1

export function AskTab() {
  const user = useSessionUser()
  const [msgs, setMsgs] = useState<Msg[]>([])
  const [q, setQ] = useState('')
  const [busy, setBusy] = useState(false)
  const [unavailable, setUnavailable] = useState<string | null>(null)
  const endRef = useRef<HTMLDivElement>(null)
  const boxRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => { endRef.current?.scrollIntoView({ behavior: 'smooth', block: 'end' }) }, [msgs])

  const ask = async (question: string) => {
    const text = question.trim()
    if (text.length < 2 || busy) return
    if (text.length > 2000) { setMsgs((m) => [...m, { id: seq++, role: 'assistant', text: 'Please keep the question under 2000 characters.', error: true }]); return }
    setQ('')
    setBusy(true)
    const pendingId = seq++
    setMsgs((m) => [...m, { id: seq++, role: 'user', text }, { id: pendingId, role: 'assistant', text: '', pending: true }])
    try {
      const r = await post<AskResponse>('/buddhi/v1/ask', { question: text })
      setMsgs((m) => m.map((x) => (x.id === pendingId ? { ...x, text: r.answer, usage: r.usage, pending: false } : x)))
    } catch (e) {
      if (isAiUnavailable(e)) { setUnavailable(errorMessage(e)); setMsgs((m) => m.filter((x) => x.id !== pendingId)) }
      else setMsgs((m) => m.map((x) => (x.id === pendingId ? { ...x, text: errorMessage(e), error: true, pending: false } : x)))
    } finally {
      setBusy(false)
      boxRef.current?.focus()
    }
  }

  if (unavailable) return <AiUnavailable message={unavailable} />

  return (
    <Card padded={false} className="flex min-h-[60vh] flex-col">
      <div className="flex-1 space-y-4 overflow-y-auto px-4 py-4 sm:px-6" aria-live="polite">
        {msgs.length === 0 ? (
          <Empty icon={Sparkles} title="Ask about your books" text="Hindi, English or Hinglish — answers come only from your own sales, stock, ledger and invoices."
            action={<div className="flex flex-wrap justify-center gap-2">{SUGGESTIONS.map((s) => <button key={s} type="button" onClick={() => ask(s)} className="rounded-full border border-slate-200 bg-white px-3 py-1.5 text-sm hover:border-brand-400 hover:bg-brand-50 cursor-pointer">{s}</button>)}</div>} />
        ) : msgs.map((m) => (
          <div key={m.id} className={`flex gap-3 ${m.role === 'user' ? 'flex-row-reverse' : ''}`}>
            <div className={`grid h-8 w-8 shrink-0 place-items-center rounded-full ${m.role === 'user' ? 'bg-slate-200 text-slate-600' : 'bg-brand-700 text-white'}`} aria-hidden>
              {m.role === 'user' ? <User size={16} /> : <Bot size={16} />}
            </div>
            <div className={`max-w-[85%] sm:max-w-[75%] ${m.role === 'user' ? 'text-right' : ''}`}>
              <div className={`inline-block whitespace-pre-wrap rounded-2xl px-4 py-2.5 text-left text-sm leading-relaxed ${m.role === 'user' ? 'bg-brand-700 text-white' : m.error ? 'bg-rose-50 text-rose-700' : 'bg-slate-100 text-ink'}`}>
                {m.pending ? <span className="inline-flex items-center gap-2 text-slate-500"><Loader2 size={14} className="animate-spin" /> Reading your books…</span> : m.text}
              </div>
              <div className={`mt-1 flex items-center gap-2 text-[11px] text-slate-400 ${m.role === 'user' ? 'justify-end' : ''}`}>
                {m.role === 'user' ? (user?.name?.split(' ')[0] ?? 'You') : 'Maapan'}
                {m.usage && <UsageChip usage={m.usage} />}
              </div>
            </div>
          </div>
        ))}
        <div ref={endRef} />
      </div>
      {msgs.length > 0 && !busy && (
        <div className="flex flex-wrap gap-2 border-t border-slate-100 px-4 py-2 sm:px-6">
          {SUGGESTIONS.slice(0, 3).map((s) => <button key={s} type="button" onClick={() => ask(s)} className="rounded-full border border-slate-200 bg-white px-2.5 py-1 text-xs text-slate-600 hover:border-brand-400 hover:bg-brand-50 cursor-pointer">{s}</button>)}
        </div>
      )}
      <form className="flex items-end gap-2 border-t border-slate-100 p-3 sm:p-4" onSubmit={(e) => { e.preventDefault(); void ask(q) }}>
        {msgs.length > 0 && <IconButton icon={RotateCcw} label="Clear conversation" onClick={() => setMsgs([])} disabled={busy} />}
        <Textarea ref={boxRef} rows={1} value={q} onChange={(e) => setQ(e.target.value)} placeholder="Ask in Hindi, English or Hinglish…" aria-label="Your question" className="max-h-32 min-h-[44px] resize-none"
          onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); void ask(q) } }} disabled={busy} maxLength={2000} />
        <Button type="submit" icon={Send} loading={busy} disabled={q.trim().length < 2}>Ask</Button>
      </form>
    </Card>
  )
}
