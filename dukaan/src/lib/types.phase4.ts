/**
 * Shapes for the phase-4 backends: bahi bill-wise / bank reconciliation / cheques / TDS / GST set-off /
 * year-end / audit, pehchaan devices + audit log, soochna messaging settings / consents / dead letters,
 * and dwaar's /api/_status. Named after the Go structs; money = integer paise unless noted.
 */

/* ---------------- bahi: bill-wise ---------------- */

/** bahi BillAllocation — one settlement of a bill by a voucher. */
export interface BillAllocation {
  id: string
  bill_id: string
  bill_ref: string
  voucher_id: string
  voucher_number: string
  voucher_type: string
  date: string
  amount_paise: number
  /** manual | auto | invoice … */
  method: string
}

/** bahi Bill (GET /v1/parties/{id}/bills). side dr = receivable, cr = payable. */
export interface Bill {
  id: string
  party_id: string
  party_name?: string
  voucher_id: string
  voucher_number: string
  voucher_type: string
  side: 'dr' | 'cr'
  ref: string
  bill_date: string
  due_date: string
  amount_paise: number
  allocated_paise: number
  outstanding_paise: number
  /** 0 when not yet due. */
  overdue_days: number
  status: string
  allocations?: BillAllocation[]
}

/** bahi OnAccount — a settling voucher's unallocated part. side cr settles receivables. */
export interface OnAccount {
  voucher_id: string
  voucher_number: string
  voucher_type: string
  date: string
  side: 'dr' | 'cr'
  amount_paise: number
  allocated_paise: number
  unallocated_paise: number
}

/** bahi BillwiseStatement (GET /v1/parties/{id}/statement?view=bills). */
export interface BillwiseStatement {
  party_id: string
  party_name: string
  as_of: string
  credit_days: number
  bills: Bill[]
  on_account: OnAccount[]
  /** Ledger balance, Dr +. */
  balance_paise: number
  bills_outstanding_paise: number
  overdue_paise: number
  /** balance − Σ signed outstanding (− = on-account credit). */
  unallocated_paise: number
}

export interface AllocationInput { bill_id: string; amount_paise: number }

/** GET|PUT /v1/vouchers/{id}/allocations. */
export interface VoucherAllocations { voucher_id: string; allocations: BillAllocation[]; allocated_paise: number }

/* ---------------- bahi: aging by due date ---------------- */

/** posting.DueBuckets. */
export interface DueBuckets {
  not_due_paise: number
  d0_30_paise: number
  d31_60_paise: number
  d61_90_paise: number
  d90_plus_paise: number
  /** Σ open bills. */
  total_paise: number
  /** Unallocated settlements (advance); reduces the net. */
  on_account_paise: number
  /** = ledger balance. */
  net_paise: number
  oldest_due_date?: string
}
export interface DueAgingRow extends DueBuckets { party_id: string; party_name: string; phone?: string; credit_days: number; open_bills: number }
export interface DueAgingReport { kind: 'receivables' | 'payables'; basis: 'due'; as_of: string; rows: DueAgingRow[]; totals: DueBuckets }

/* ---------------- bahi: store P&L ---------------- */

export interface StorePL { store_id: string; income_paise: number; expenses_paise: number; gross_profit_paise: number; net_profit_paise: number }
export interface StorePLReport { from: string; to: string; stores: StorePL[] }

/* ---------------- bahi: bank statements & BRS ---------------- */

export interface StatementPreset { code: string; name: string; date: string[]; value_date: string[]; description: string[]; reference: string[]; debit: string[]; credit: string[]; amount?: string[]; drcr?: string[]; balance: string[] }

/** bankstmt.Mapping — overrides any preset column (header text). */
export interface StatementMapping { date?: string; value_date?: string; description?: string; reference?: string; debit?: string; credit?: string; amount?: string; drcr?: string; balance?: string; date_format?: string }

/** bahi StatementImport. */
export interface StatementImport { id: string; bank_account_id: string; preset: string; file_name?: string; from: string; to: string; lines: number; duplicates: number; matched: number }

export type BankLineStatus = 'unmatched' | 'matched' | 'ignored'

/** bahi BankLine (GET /v1/bank/statement-lines). */
export interface BankLine {
  id: string
  statement_id: string
  bank_account_id: string
  date: string
  value_date?: string
  description: string
  reference?: string
  withdrawal_paise: number
  deposit_paise: number
  balance_paise?: number
  status: BankLineStatus
  entry_id?: string
  voucher_id?: string
  voucher_number?: string
  match_rule?: string
}

export interface BRSItem { date: string; voucher_id?: string; voucher_number?: string; line_id?: string; description: string; reference?: string; amount_paise: number }

/** bahi BRS (GET /v1/reports/brs). */
export interface BRS {
  bank_account_id: string
  account_name: string
  as_of: string
  from?: string
  balance_per_books_paise: number
  unpresented_cheques: BRSItem[] | null
  uncredited_deposits: BRSItem[] | null
  bank_debits_not_in_books: BRSItem[] | null
  bank_credits_not_in_books: BRSItem[] | null
  unpresented_paise: number
  uncredited_paise: number
  bank_debits_not_in_books_paise: number
  bank_credits_not_in_books_paise: number
  balance_per_bank_computed_paise: number
  balance_per_statement_paise?: number
  difference_paise?: number
}

/* ---------------- bahi: PDC ---------------- */

export type PdcBucket = 'overdue' | 'due_soon' | 'later' | 'awaiting_clearance'

/** One row of GET /v1/cheques/pdc (a Cheque + its bucket). */
export interface PdcItem {
  id: string
  direction: 'received' | 'issued'
  party_id: string
  party_name: string
  bank_account_id: string
  cheque_no: string
  cheque_date: string
  drawee_bank?: string
  amount_paise: number
  status: string
  post_dated: boolean
  /** Negative = past the cheque date. */
  days_to_due: number
  bucket: PdcBucket
}
export interface PdcReport { as_of: string; days: number; items: PdcItem[]; totals_paise: Partial<Record<'received' | 'issued', Partial<Record<PdcBucket, number>>>> }

/* ---------------- bahi: TDS / TCS ---------------- */

export interface TdsSettings { tan: string; deductor_name: string; auto_194q: boolean }

export interface TdsSection {
  code: string
  name: string
  kind: 'tds' | 'tcs'
  rate_bp: number
  rate_individual_bp: number
  no_pan_rate_bp: number
  single_threshold_paise?: number
  aggregate_threshold_paise?: number
  excess_only: boolean
  code_26q: string
  valid_until?: string
}

/** posting.TDSResult. */
export interface TdsResult {
  section: string
  applicable: boolean
  base_paise: number
  taxable_paise: number
  rate_bp: number
  tds_paise: number
  higher_rate_no_pan: boolean
  aggregate_paise: number
  reason: string
}
export interface TdsCompute { party_id: string; party_name: string; pan: string; fy: string; section: TdsSection; result: TdsResult }

export interface TdsRequest {
  party_id: string
  section?: string
  date?: string
  base_paise: number
  direction?: 'payable' | 'receivable'
  tds_paise?: number
  ref_voucher_id?: string
  key?: string
  narration?: string
}

/** bahi TDSEntry. */
export interface TdsEntry {
  id: string
  kind: 'tds' | 'tcs'
  direction: 'payable' | 'receivable'
  section: string
  party_id: string
  party_name: string
  pan?: string
  date: string
  fy: string
  base_paise: number
  taxable_paise: number
  rate_bp: number
  tds_paise: number
  higher_rate: boolean
  voucher_id?: string
  ref_voucher_id?: string
  challan_id?: string
  status: string
}

export interface TdsSummaryLine { kind: string; direction: string; section: string; entries: number; base_paise: number; tds_paise: number; deposited_paise: number; pending_paise: number }
export interface TdsSummary { fy: string; lines: TdsSummaryLine[] | null }

export interface ChallanInput { kind: 'tds' | 'tcs'; bsr_code: string; challan_no: string; paid_on: string; bank_account_id: string; entry_ids?: string[]; month?: string; section?: string; interest_paise?: number }
export interface Challan { id: string; kind: string; bsr_code: string; challan_no: string; paid_on: string; amount_paise: number; interest_paise: number; bank_account_id: string; voucher_id: string; entries: number }

export interface Form26Q { fy: string; quarter: number; from: string; to: string; tan: string; rows: string[][] | null; entries_without_challan: number }

export interface Tcs1HRow { party_id: string; party_name: string; pan?: string; receipts_paise: number; sales_paise: number; receipts_above_threshold_paise: number; tcs_collected_paise: number; tcs_due_paise: number; threshold_crossed: boolean }
export interface Tcs1HReport { from: string; to: string; section: TdsSection; applicable: boolean; note?: string; rows: Tcs1HRow[] | null }

/* ---------------- bahi: GST set-off, year-end, audit ---------------- */

export interface SetOffInput { gstin?: string; period: string; date?: string; pay_from_bank_account_id?: string }
export interface SetOffResult {
  gstin: string
  period: string
  date: string
  setoff_voucher?: { id: string; number: string }
  cash_voucher?: { id: string; number: string }
  warnings?: string[] | null
  computation_source?: string
}
export interface SetOffRecord { gstin: string; period: string; setoff_voucher_id?: string; cash_voucher_id?: string; created_by: string; created_at: string }

export interface YearEnd {
  fy: string
  from: string
  to: string
  voucher_id?: string
  voucher_number?: string
  status: 'closed' | 'reopened'
  net_profit_paise: number
  drawings_closed_paise: number
  closed_by?: string
  locked_until?: string
}

/** bahi AuditEntry (GET /v1/audit). */
export interface LedgerAuditEntry { id: number; at: string; actor: string; action: string; entity: string; entity_id: string; before?: unknown; after?: unknown }

/* ---------------- pehchaan ---------------- */

export type DevicePlatform = 'android' | 'ios' | 'windows' | 'linux' | 'macos' | 'web' | 'other'

/** pehchaan Device (GET /v1/devices). */
export interface PosDevice {
  id: string
  tenant_id: string
  store_id: string
  device_id: string
  name?: string
  platform?: DevicePlatform
  status: 'active' | 'revoked'
  registered_by?: string
  revoked_by?: string
  revoked_at?: string
  last_seen_at?: string
  created_at: string
}
export interface DeviceInput { device_id: string; store_id: string; name?: string; platform?: DevicePlatform }

/** pehchaan AuditEntry (GET /v1/audit). */
export interface IdentityAuditEntry { id: number; actor_id: string; actor_name?: string; action: string; target?: string; details: unknown; ip?: string; created_at: string }
export interface IdentityAuditPage { items: IdentityAuditEntry[]; next_before?: number }

/* ---------------- soochna ---------------- */

export type MsgChannel = 'sms' | 'whatsapp' | 'email'

export interface TenantTemplate { template: string; channel: 'sms' | 'whatsapp'; provider_template_id: string }

/** soochna TenantSenders (GET|PUT /v1/settings/messaging). Empty fields = Maapan defaults. */
export interface MessagingSettings {
  sms_sender_id: string
  whatsapp_source: string
  whatsapp_app_name: string
  email_from: string
  email_from_name: string
  email_reply_to: string
  payment_reminders: boolean
  reminder_days: number[] | null
  reminder_channels: string[] | null
  templates: TenantTemplate[] | null
  updated_at?: string
  updated_by?: string
}

export interface TemplateInfo { name: string; category: string; params: string[]; channels: string[] | null }
export interface TemplatesResponse { templates: string[]; details: TemplateInfo[] }

export type ConsentStatus = 'opted_out' | 'opted_in' | 'dnd'
/** soochna Consent. scope = "global" (STOP keywords) or the tenant id. */
export interface Consent { scope: string; address: string; channel: string; status: ConsentStatus; source: string; note?: string; updated_at: string }
export interface ConsentInput { address: string; channel: 'sms' | 'whatsapp' | 'email' | 'all'; status: ConsentStatus; note?: string }

export interface DeadLetter {
  id: string
  message_id: string
  tenant_id?: string
  channel: string
  template: string
  to: string
  attempts: number
  last_error?: string
  reason: string
  dead_at: string
  replay_count: number
  replayed_at?: string
  replayed_by?: string
}
export interface ReplayInput { ids?: string[]; template?: string; channel?: string; reason?: string; limit?: number }
export interface ReplayResult { replayed: number; statuses: Record<string, number>; message_ids: string[] | null }

/* ---------------- dwaar ---------------- */

export type UpstreamState = 'up' | 'down' | 'starting' | 'not_configured'
export interface GatewayStatus {
  status: 'ok' | 'degraded'
  checked_at: string
  rate_limiter: string
  services: Record<string, { status: UpstreamState | string; latency_ms?: number }>
}
