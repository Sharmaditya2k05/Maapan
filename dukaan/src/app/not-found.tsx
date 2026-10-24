import Link from 'next/link'
import { Logo } from '@/components/Logo'

export default function NotFound() {
  return (
    <div className="grid min-h-dvh place-items-center p-6 text-center">
      <div>
        <Logo size={40} />
        <h1 className="mt-6 text-2xl font-extrabold">Page not found</h1>
        <p className="mt-2 text-sm text-slate-500">The link may be old or the page may have moved.</p>
        <Link href="/app" className="link mt-6 inline-block">Back to the dashboard</Link>
      </div>
    </div>
  )
}
