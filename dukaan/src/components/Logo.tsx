/* eslint-disable @next/next/no-img-element */
/**
 * Brand assets cut from the master artwork in /public/logo.png (the Maapan
 * scales logo): a horizontal lockup of the scales and the MAAPAN wordmark,
 * a white variant for dark surfaces, and the scales on a tile as the app icon.
 */

/** Square app icon: the scales on a light tile. */
export function LogoMark({ size = 36, className = '' }: { size?: number; className?: string }) {
  return <img src="/brand/mark-512.png" width={size} height={size} style={{ width: size, height: size }} alt="" aria-hidden className={`shrink-0 select-none ${className}`} draggable={false} />
}

/**
 * Full brand lockup. `size` scales the wordmark, `light` swaps to the white
 * wordmark for dark backgrounds, `tagline` adds “GST | Billing | Accounting”.
 */
export function Logo({ size = 36, light = false, tagline = false }: { size?: number; light?: boolean; tagline?: boolean }) {
  const height = Math.round(size * 0.8) // lockup art is 696×122
  const width = Math.round((height * 696) / 122)
  const bar = <span className="inline-block h-[1em] w-[1.5px] rounded bg-brand-500" />
  return (
    <div className="inline-flex select-none flex-col items-start">
      <img src={`/brand/wordmark${light ? '-light' : ''}.png`} alt="Maapan" style={{ height, width }} className="block" draggable={false} />
      {tagline && (
        <div className={`font-logo mt-[0.5em] flex items-center gap-[0.5em] whitespace-nowrap font-medium leading-none ${light ? 'text-slate-300' : 'text-brand-800'}`}
          style={{ fontSize: Math.max(9, +(width * 0.074).toFixed(1)), paddingLeft: '0.25em', letterSpacing: '0.08em' }}>
          <span>GST</span>{bar}<span>Billing</span>{bar}<span>Accounting</span>
        </div>
      )}
    </div>
  )
}
