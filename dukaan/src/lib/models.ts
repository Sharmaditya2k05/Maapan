/**
 * The fourteen Maapan models (services, the shared library, this app and ops).
 * `health` is the gateway path of a service's health probe; models without one
 * are not separately deployed.
 */
export interface Model {
  key: string
  role: string
  kind: 'service' | 'gateway' | 'library' | 'app' | 'ops'
  health?: string
}

export const MODELS: Model[] = [
  { key: 'dukaan', role: 'This app: POS counter, billing and back-office screens', kind: 'app' },
  { key: 'dwaar', role: 'API gateway: routing, sign-in checks, rate limits', kind: 'gateway' },
  { key: 'pehchaan', role: 'Identity: OTP login, users, roles, stores, GST registrations', kind: 'service', health: '/pehchaan/healthz' },
  { key: 'aadesh', role: 'Billing: sales, purchases, returns, numbering, offline sync', kind: 'service', health: '/aadesh/healthz' },
  { key: 'bahi', role: 'Accounting: double-entry ledgers, parties, reports', kind: 'service', health: '/bahi/healthz' },
  { key: 'kar', role: 'GST: GSTIN check, HSN, e-invoice, e-way bill, GSTR-1/3B, 2B', kind: 'service', health: '/kar/healthz' },
  { key: 'lenden', role: 'Payments: UPI QR, payment links, refunds, settlements', kind: 'service', health: '/lenden/healthz' },
  { key: 'bhandar', role: 'Inventory: items, stock, batches, transfers, re-order', kind: 'service', health: '/bhandar/healthz' },
  { key: 'nirmaan', role: 'Manufacturing: bills of materials and production', kind: 'service', health: '/nirmaan/healthz' },
  { key: 'soochna', role: 'Notifications: SMS and WhatsApp messages', kind: 'service', health: '/soochna/healthz' },
  { key: 'aayaat', role: 'Data import: MargBooks parties, balances and items', kind: 'service', health: '/aayaat/healthz' },
  { key: 'buddhi', role: 'AI: bill scanning, Ask Maapan, forecasts, anomalies', kind: 'service', health: '/buddhi/healthz' },
  { key: 'mool', role: 'Shared library: GST maths, money, events, auth (inside every service)', kind: 'library' },
  { key: 'sanchalan', role: 'Operations: deployment, CI, database and messaging setup', kind: 'ops' },
]
