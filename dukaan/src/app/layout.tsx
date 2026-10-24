import type { Metadata, Viewport } from 'next'
import { JetBrains_Mono, Outfit, Plus_Jakarta_Sans } from 'next/font/google'
import './globals.css'
import { Providers } from '@/components/providers'

const jakarta = Plus_Jakarta_Sans({ subsets: ['latin'], variable: '--font-jakarta', display: 'swap' })
const outfit = Outfit({ subsets: ['latin'], variable: '--font-outfit', weight: ['500', '600', '700', '800'], display: 'swap' })
const jetbrains = JetBrains_Mono({ subsets: ['latin'], variable: '--font-jetbrains', display: 'swap' })

export const metadata: Metadata = {
  title: { default: 'Taraju', template: '%s · Taraju' },
  description: 'GST billing, accounting and inventory for Indian retail.',
  applicationName: 'Taraju',
  manifest: '/manifest.webmanifest',
  icons: { icon: '/favicon-64.png', apple: '/apple-touch-icon.png' },
  appleWebApp: { capable: true, title: 'Taraju', statusBarStyle: 'default' },
}

export const viewport: Viewport = {
  themeColor: '#062c60',
  width: 'device-width',
  initialScale: 1,
  viewportFit: 'cover',
}

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en-IN" className={`${jakarta.variable} ${outfit.variable} ${jetbrains.variable}`}>
      <body className="min-h-dvh">
        <Providers>{children}</Providers>
      </body>
    </html>
  )
}
