'use client'

import { Suspense, useEffect, useRef, useState } from 'react'
import { useRouter, useSearchParams } from 'next/navigation'
import { ArrowRight, MessageSquare, Phone, ShieldCheck } from 'lucide-react'
import { Logo } from '@/components/Logo'
import { Button, ErrorBox, Field, Input } from '@/components/ui'
import { api, errorMessage } from '@/lib/api'
import { session } from '@/lib/session'
import { refreshSessionUser } from '@/lib/pehchaan'
import type { Membership, VerifyResponse } from '@/lib/types'

interface OTPRequested { phone: string; channel: string; expires_in_sec: number }

function LoginForm() {
  const router = useRouter()
  const params = useSearchParams()
  const next = params.get('next') || '/app'
  const [step, setStep] = useState<'phone' | 'otp' | 'tenant'>('phone')
  const [phone, setPhone] = useState('')
  const [channel, setChannel] = useState<'sms' | 'whatsapp'>('sms')
  const [otp, setOtp] = useState('')
  const [masked, setMasked] = useState('')
  const [memberships, setMemberships] = useState<Membership[]>([])
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [left, setLeft] = useState(0)
  const otpRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (session.accessToken()) router.replace(next)
  }, [router, next])
  useEffect(() => {
    if (left <= 0) return
    const t = setTimeout(() => setLeft((l) => l - 1), 1000)
    return () => clearTimeout(t)
  }, [left])

  const requestOTP = async () => {
    setErr('')
    const p = phone.replace(/\D/g, '')
    if (p.length < 10) return setErr('Enter your 10-digit mobile number.')
    setBusy(true)
    try {
      const r = await api<OTPRequested>('/pehchaan/v1/auth/otp/request', { method: 'POST', body: { phone: p, channel }, auth: false })
      setMasked(r.phone)
      setLeft(r.expires_in_sec || 300)
      setStep('otp')
      setTimeout(() => otpRef.current?.focus(), 50)
    } catch (e) {
      setErr(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  const finish = async (r: VerifyResponse) => {
    if (r.needs_onboarding) {
      sessionStorage.setItem('maapan.onboarding', JSON.stringify({ token: r.onboarding_token, phone: phone.replace(/\D/g, '') }))
      router.replace('/onboarding')
      return
    }
    if (r.access_token && r.refresh_token) {
      session.setTokens({ access_token: r.access_token, refresh_token: r.refresh_token })
      await refreshSessionUser()
      router.replace(next)
    }
  }

  const verify = async (tenantID?: string) => {
    setErr('')
    if (otp.replace(/\D/g, '').length !== 6) return setErr('Enter the 6-digit code.')
    setBusy(true)
    try {
      const r = await api<VerifyResponse>('/pehchaan/v1/auth/otp/verify', {
        method: 'POST', auth: false, body: { phone: phone.replace(/\D/g, ''), otp: otp.replace(/\D/g, ''), tenant_id: tenantID },
      })
      // A phone that belongs to several businesses must pick one.
      if (!r.needs_onboarding && !r.access_token && (r.memberships?.length ?? 0) > 1) {
        setMemberships(r.memberships ?? [])
        setStep('tenant')
        return
      }
      await finish(r)
    } catch (e) {
      setErr(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="card w-full max-w-md p-6 sm:p-8">
      <Logo size={40} tagline />
      {step === 'phone' && (
        <form className="mt-8 space-y-4" onSubmit={(e) => { e.preventDefault(); void requestOTP() }}>
          <div>
            <h1 className="text-xl font-extrabold">Sign in</h1>
            <p className="mt-1 text-sm text-slate-500">We will send a one-time code to your mobile.</p>
          </div>
          <Field label="Mobile number">
            <div className="relative">
              <Phone size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" />
              <Input autoFocus inputMode="numeric" autoComplete="tel" className="pl-9" placeholder="98765 43210" value={phone} onChange={(e) => setPhone(e.target.value)} />
            </div>
          </Field>
          <div className="flex gap-2 text-xs">
            {(['sms', 'whatsapp'] as const).map((c) => (
              <button key={c} type="button" onClick={() => setChannel(c)} className={`flex items-center gap-1.5 rounded-lg px-3 py-1.5 font-semibold ring-1 ring-inset cursor-pointer ${channel === c ? 'bg-brand-50 text-brand-700 ring-brand-600/20' : 'text-slate-500 ring-slate-200'}`}>
                <MessageSquare size={13} /> {c === 'sms' ? 'SMS' : 'WhatsApp'}
              </button>
            ))}
          </div>
          {err && <ErrorBox message={err} />}
          <Button type="submit" size="lg" className="w-full" loading={busy} icon={ArrowRight}>Send code</Button>
        </form>
      )}
      {step === 'otp' && (
        <form className="mt-8 space-y-4" onSubmit={(e) => { e.preventDefault(); void verify() }}>
          <div>
            <h1 className="text-xl font-extrabold">Enter the code</h1>
            <p className="mt-1 text-sm text-slate-500">Sent to {masked || phone}{left > 0 ? ` · valid for ${Math.floor(left / 60)}:${String(left % 60).padStart(2, '0')}` : ''}.</p>
          </div>
          <Field label="6-digit code">
            <div className="relative">
              <ShieldCheck size={16} className="pointer-events-none absolute left-3 top-1/2 -translate-y-1/2 text-slate-400" />
              <Input ref={otpRef} inputMode="numeric" autoComplete="one-time-code" maxLength={6} className="num pl-9 text-lg tracking-[0.4em]" value={otp}
                onChange={(e) => { const v = e.target.value.replace(/\D/g, '').slice(0, 6); setOtp(v) }} />
            </div>
          </Field>
          {err && <ErrorBox message={err} />}
          <Button type="submit" size="lg" className="w-full" loading={busy}>Verify and continue</Button>
          <div className="flex justify-between text-xs text-slate-500">
            <button type="button" className="link" onClick={() => { setStep('phone'); setOtp(''); setErr('') }}>Change number</button>
            <button type="button" className="link disabled:opacity-40" disabled={left > 240 || busy} onClick={requestOTP}>Resend code</button>
          </div>
        </form>
      )}
      {step === 'tenant' && (
        <div className="mt-8 space-y-4">
          <div>
            <h1 className="text-xl font-extrabold">Choose a business</h1>
            <p className="mt-1 text-sm text-slate-500">This number is linked to more than one business.</p>
          </div>
          <ul className="divide-y divide-slate-100 overflow-hidden rounded-2xl border border-slate-200">
            {memberships.map((m) => (
              <li key={m.id}>
                <button type="button" disabled={busy} onClick={() => verify(m.tenant_id)} className="flex w-full items-center justify-between px-4 py-3 text-left text-sm hover:bg-slate-50 cursor-pointer">
                  <span className="font-semibold">{m.business_name || m.tenant_id}</span>
                  <span className="text-xs text-slate-500">{m.role.replace('_', ' ')}</span>
                </button>
              </li>
            ))}
          </ul>
          {err && <ErrorBox message={err} />}
        </div>
      )}
      <p className="mt-8 text-center text-[11px] text-slate-400">
        New to Maapan? Sign in with your mobile number and we will set up your business in a minute.
      </p>
    </div>
  )
}

export default function LoginPage() {
  return (
    <div className="grid min-h-dvh place-items-center bg-gradient-to-br from-slate-50 via-white to-brand-50 p-4">
      <Suspense fallback={null}><LoginForm /></Suspense>
      <div className="fixed bottom-3 text-[11px] text-slate-400">© Maapan · GST | Billing | Accounting</div>
    </div>
  )
}
