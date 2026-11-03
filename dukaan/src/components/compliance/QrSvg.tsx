'use client'

/**
 * A QR code drawn as inline SVG, synchronously (qrcode's create() gives the module matrix), so it
 * is present the moment a print page renders — no async data URL racing window.print().
 * Used for the e-invoice signed QR (Rule 48(4): the QR must appear on the printed invoice).
 */
import { useMemo } from 'react'
import QRCode from 'qrcode'

export function QrSvg({ text, size = 160, className = '', title = 'QR code', level = 'M' }: { text: string; size?: number | string; className?: string; title?: string; level?: 'L' | 'M' | 'Q' | 'H' }) {
  const matrix = useMemo(() => {
    try {
      const m = QRCode.create(text, { errorCorrectionLevel: level }).modules
      let d = ''
      for (let r = 0; r < m.size; r++) {
        for (let c = 0; c < m.size; c++) if (m.data[r * m.size + c]) d += `M${c + 4} ${r + 4}h1v1h-1z`
      }
      return { size: m.size + 8, d }
    } catch {
      return null
    }
  }, [text, level])
  if (!matrix) return <div className={`grid place-items-center text-[10px] text-rose-600 ${className}`} style={{ width: size, height: size }}>QR too long to draw</div>
  return (
    <svg role="img" aria-label={title} viewBox={`0 0 ${matrix.size} ${matrix.size}`} width={size} height={size} shapeRendering="crispEdges" className={className}>
      <title>{title}</title>
      <rect width={matrix.size} height={matrix.size} fill="#fff" />
      <path d={matrix.d} fill="#000" />
    </svg>
  )
}
