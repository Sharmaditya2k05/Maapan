'use client'

import { useEffect, useMemo, useState } from 'react'
import { useRouter } from 'next/navigation'
import { Building2, Store } from 'lucide-react'
import { Logo } from '@/components/Logo'
import { Button, ErrorBox, Field, Input, Select, Textarea } from '@/components/ui'
import { api, errorMessage } from '@/lib/api'
import { session } from '@/lib/session'
import { refreshSessionUser } from '@/lib/pehchaan'
import { STATES, validateGSTIN } from '@/lib/gst'
import type { PehchaanSession } from '@/lib/types'

interface Handoff { token: string; phone: string }

export default function OnboardingPage() {
  const router = useRouter()
  const [handoff, setHandoff] = useState<Handoff | null>(null)
  const [form, setForm] = useState({ owner_name: '', business_name: '', gstin: '', state_code: '', store_name: 'Main store', address: '' })
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const set = (k: keyof typeof form, v: string) => setForm((f) => ({ ...f, [k]: v }))

  useEffect(() => {
    try {
      const raw = sessionStorage.getItem('taraju.onboarding')
      if (!raw) { router.replace('/login'); return }
      setHandoff(JSON.parse(raw))
    } catch { router.replace('/login') }
  }, [router])

  // Typing a GSTIN fills the state from its first two digits.
  const gstinError = useMemo(() => (form.gstin ? validateGSTIN(form.gstin) : null), [form.gstin])
  useEffect(() => {
    const g = form.gstin.trim().toUpperCase()
    if (g.length >= 2 && STATES[g.slice(0, 2)] && !form.state_code) setForm((f) => ({ ...f, state_code: g.slice(0, 2) }))
  }, [form.gstin, form.state_code])

  const states = useMemo(() => Object.entries(STATES).sort((a, b) => a[1].localeCompare(b[1])), [])

  const submit = async () => {
    setErr('')
    if (!form.owner_name.trim()) return setErr('Your name is required.')
    if (!form.business_name.trim()) return setErr('Business name is required.')
    if (!form.state_code) return setErr('Choose the state your business is registered in.')
    if (gstinError) return setErr(gstinError)
    if (!handoff) return
    setBusy(true)
    try {
      const sess = await api<PehchaanSession>('/pehchaan/v1/onboarding', {
        method: 'POST', auth: false, headers: { Authorization: `Bearer ${handoff.token}` },
        body: { ...form, gstin: form.gstin.trim().toUpperCase() || undefined, address: form.address.trim() || undefined },
      })
      sessionStorage.removeItem('taraju.onboarding')
      session.setTokens({ access_token: sess.access_token, refresh_token: sess.refresh_token })
      await refreshSessionUser()
      router.replace('/app')
    } catch (e) {
      setErr(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="grid min-h-dvh place-items-center bg-gradient-to-br from-slate-50 via-white to-brand-50 p-4">
      <form className="card w-full max-w-xl p-6 sm:p-8" onSubmit={(e) => { e.preventDefault(); void submit() }}>
        <Logo size={40} />
        <h1 className="mt-6 text-xl font-extrabold">Set up your business</h1>
        <p className="mt-1 text-sm text-slate-500">Takes a minute. You can add more stores and people later from Settings.</p>

        <div className="mt-6 grid gap-4 sm:grid-cols-2">
          <Field label="Your name" className="sm:col-span-2"><Input autoFocus value={form.owner_name} onChange={(e) => set('owner_name', e.target.value)} placeholder="e.g. Rahul Sharma" /></Field>
          <Field label="Business name" className="sm:col-span-2">
            <div className="relative"><Building2 size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" /><Input className="pl-9" value={form.business_name} onChange={(e) => set('business_name', e.target.value)} placeholder="Sharma General Store" /></div>
          </Field>
          <Field label="GSTIN (optional)" error={gstinError} hint={!gstinError ? 'Leave empty if you are not registered under GST.' : undefined}>
            <Input className="font-mono uppercase" value={form.gstin} onChange={(e) => set('gstin', e.target.value.toUpperCase())} placeholder="27AAPFU0939F1ZV" maxLength={15} />
          </Field>
          <Field label="State">
            <Select value={form.state_code} onChange={(e) => set('state_code', e.target.value)}>
              <option value="">Select state</option>
              {states.map(([code, name]) => <option key={code} value={code}>{name} ({code})</option>)}
            </Select>
          </Field>
          <Field label="First store name">
            <div className="relative"><Store size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" /><Input className="pl-9" value={form.store_name} onChange={(e) => set('store_name', e.target.value)} /></div>
          </Field>
          <Field label="Address (needed on invoices)" className="sm:col-span-2">
            <Textarea rows={2} value={form.address} onChange={(e) => set('address', e.target.value)} placeholder="Shop no., street, city, PIN" />
          </Field>
        </div>
        {err && <div className="mt-4"><ErrorBox message={err} /></div>}
        <div className="mt-6 flex items-center justify-between gap-3">
          <span className="text-xs text-slate-500">Signing in as {handoff?.phone ?? '…'}</span>
          <Button type="submit" size="lg" loading={busy}>Create my business</Button>
        </div>
      </form>
    </div>
  )
}
