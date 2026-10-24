'use client'

import { useEffect, useState } from 'react'
import { Hash, Printer, RefreshCw } from 'lucide-react'
import { Badge, Button, Card, Field, Select, Toggle, useToast } from '@/components/ui'
import { useOnline, useStore } from '@/lib/hooks'
import { blockStatus, ensureBlock } from '@/lib/offline'
import { usePrefs, type Prefs } from '@/lib/prefs'
import { deviceId } from '@/lib/session'
import { useCopy } from '@/components/stock/hooks'

/** Per-device preferences (localStorage) and the offline invoice-number block for the active store. */
export function DeviceSection() {
  const [p, setPrefs] = usePrefs()
  const { storeId, store } = useStore()
  const online = useOnline()
  const toast = useToast()
  const copy = useCopy()
  const [id, setId] = useState('')
  const [block, setBlock] = useState<{ left: number; total: number } | null>(null)
  const [busy, setBusy] = useState(false)

  useEffect(() => { setId(deviceId()) }, [])
  useEffect(() => {
    if (!storeId) return
    let alive = true
    blockStatus(storeId).then((b) => alive && setBlock(b)).catch(() => alive && setBlock({ left: 0, total: 0 }))
    return () => { alive = false }
  }, [storeId])

  const reserve = async () => {
    if (!storeId) return
    setBusy(true)
    try {
      const b = await ensureBlock(storeId)
      setBlock(await blockStatus(storeId))
      toast(b ? `Numbers reserved: ${b.prefix}/${b.fy}/${String(b.next).padStart(5, '0')} onwards` : 'Could not reserve numbers', b ? 'success' : 'error')
    } finally { setBusy(false) }
  }

  return (
    <div className="space-y-4">
      <Card title="Printer & counter">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Receipt printer" hint="Sets the receipt width; A4 prints a tax invoice">
            <Select value={p.printer} onChange={(e) => setPrefs({ printer: e.target.value as Prefs['printer'] })}>
              <option value="thermal80">Thermal 80 mm</option><option value="thermal58">Thermal 58 mm</option><option value="a4">A4 laser / inkjet</option>
            </Select>
          </Field>
          <Field label="Default tender" hint="Pre-selected payment mode at the counter">
            <Select value={p.defaultTender} onChange={(e) => setPrefs({ defaultTender: e.target.value as Prefs['defaultTender'] })}>
              <option value="cash">Cash</option><option value="upi">UPI</option><option value="card">Card</option>
            </Select>
          </Field>
          <div className="space-y-3 sm:col-span-2">
            <Toggle checked={p.autoPrint} onChange={(v) => setPrefs({ autoPrint: v })} label="Open the print dialog as soon as a bill is saved" />
            <Toggle checked={p.askPhone} onChange={(v) => setPrefs({ askPhone: v })} label="Ask for the customer's phone before saving (WhatsApp receipts)" />
            <Toggle checked={p.beep} onChange={(v) => setPrefs({ beep: v })} label="Beep on a successful barcode scan" />
          </div>
        </div>
        <p className="mt-4 text-xs text-slate-500"><Printer size={12} className="mr-1 inline" />These settings live in this browser only; every counter device keeps its own.</p>
      </Card>

      <Card title="This device">
        <div className="grid gap-4 sm:grid-cols-2">
          <div><div className="label">Device ID</div><div className="flex items-center gap-2"><code className="font-mono text-sm">{id || '…'}</code>{id && <Button size="sm" variant="ghost" onClick={() => copy(id, 'Device ID copied')}>Copy</Button>}</div><p className="mt-1 text-xs text-slate-500">Identifies this browser on offline bills and reserved number blocks.</p></div>
          <div><div className="label">Connection</div><Badge tone={online ? 'green' : 'red'}>{online ? 'online' : 'offline'}</Badge></div>
          <div className="sm:col-span-2">
            <div className="label">Offline invoice numbers · {store?.name ?? 'active store'}</div>
            <div className="flex flex-wrap items-center gap-3">
              <span className="inline-flex items-center gap-2 text-sm"><Hash size={16} className="text-brand-600" />{block ? (block.total ? <><span className="num font-semibold">{block.left}</span> of {block.total} left on this device</> : 'No block reserved yet') : 'Checking…'}</span>
              <Button size="sm" variant="secondary" icon={RefreshCw} onClick={reserve} loading={busy} disabled={!online || !storeId}>Reserve numbers now</Button>
            </div>
            <p className="mt-1 text-xs text-slate-500">The counter reserves a block of sales invoice numbers from the server so bills can be numbered while offline; a fresh block is fetched automatically when fewer than 20 % remain. Reserve one before a known outage.</p>
          </div>
        </div>
      </Card>
    </div>
  )
}
