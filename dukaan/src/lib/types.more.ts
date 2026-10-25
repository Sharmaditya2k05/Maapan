/**
 * API shapes for nirmaan (manufacturing), aayaat (data import) and buddhi (AI & insights).
 * Named after the Go structs in each service's internal/ packages. Money = paise, rates = basis points.
 */

/* ======================= nirmaan ======================= */

export type BomStatus = 'draft' | 'active' | 'archived'

/** nirmaan bom.Component. Qty is per output_qty of the parent BOM. */
export interface BomComponent {
  line_no?: number
  item_id: string
  item_name: string
  qty: number
  unit: string
  scrap_percent: number
  is_sub_assembly: boolean
  /** Pins a specific BOM; empty = the component item's active BOM. */
  sub_bom_id?: string | null
}

/** nirmaan bom.Operation — labour/overhead cost per batch of output_qty. */
export interface BomOperation {
  line_no?: number
  name: string
  cost_paise: number
}

/** nirmaan bom.BOM (GET /v1/boms/{id}). */
export interface Bom {
  id: string
  item_id: string
  item_name: string
  version: number
  output_qty: number
  unit: string
  status: BomStatus
  notes?: string
  created_by: string
  created_at: string
  updated_at: string
  components: BomComponent[]
  operations: BomOperation[]
}

/** nirmaan bom.Summary (GET /v1/boms rows). */
export interface BomSummary {
  id: string
  item_id: string
  item_name: string
  version: number
  output_qty: number
  unit: string
  status: BomStatus
  components: number
  updated_at: string
}

/** nirmaan bom.Input (POST /v1/boms, PUT /v1/boms/{id}). */
export interface BomInput {
  item_id: string
  item_name: string
  output_qty: number
  unit?: string
  notes?: string
  components: BomComponent[]
  operations?: BomOperation[]
}

/** nirmaan list envelope: no total, page by limit/offset. */
export interface NirmaanList<T> { items: T[]; limit?: number; offset?: number }

/** nirmaan bom.Node — one line of the indented explosion tree. */
export interface BomNode {
  item_id: string
  item_name: string
  qty: number
  unit: string
  level: number
  is_sub_assembly: boolean
  bom_id?: string
  children?: BomNode[]
}

/** nirmaan bom.Requirement — flattened raw-material need. */
export interface BomRequirement { item_id: string; item_name: string; unit: string; qty: number }

/** GET /v1/boms/{id}/explode. */
export interface BomExplode { tree: BomNode; requirements: BomRequirement[] }

/** nirmaan bom.CostLine. */
export interface BomCostLine {
  item_id: string
  item_name: string
  qty: number
  unit_cost_paise: number
  extended_paise: number
  is_sub_assembly: boolean
}

/** nirmaan bom.CostResult (GET /v1/boms/{id}/cost). */
export interface BomCost {
  bom_id: string
  qty: number
  material_paise: number
  operations_paise: number
  total_paise: number
  unit_cost_paise: number
  lines: BomCostLine[]
  /** Leaf item ids costed at 0 because bhandar has no purchase price. */
  missing_prices?: string[]
}

export type RunStatus = 'planned' | 'in_progress' | 'completed' | 'cancelled'

/** nirmaan production.Line. */
export interface RunLine {
  line_no: number
  kind: 'consume' | 'produce'
  item_id: string
  item_name: string
  unit: string
  is_sub_assembly: boolean
  planned_qty: number
  actual_qty?: number
  unit_cost_paise?: number
}

/** nirmaan production.Shortage. */
export interface RunShortage { item_id: string; item_name: string; required: number; on_hand: number; short: number }

/** nirmaan production.Run. */
export interface ProductionRun {
  id: string
  bom_id: string
  item_id: string
  item_name: string
  store_id: string
  qty: number
  output_qty?: number
  planned_date: string
  status: RunStatus
  output_batch_no?: string
  total_cost_paise?: number
  notes?: string
  created_by: string
  created_at: string
  started_at?: string
  completed_at?: string
  cancelled_at?: string
  lines?: RunLine[]
  shortages?: RunShortage[]
}

/** nirmaan production.CreateInput. */
export interface RunCreateInput { bom_id: string; qty: number; store_id?: string; planned_date?: string; notes?: string }

/** nirmaan production.CompleteInput. */
export interface RunCompleteInput {
  output_qty?: number
  output_batch_no?: string
  consumed?: { item_id: string; qty: number }[]
}

/* ======================= aayaat ======================= */

export type ImportKind = 'parties' | 'items' | 'opening_balances' | 'opening_stock'
export type ImportStatus = 'uploaded' | 'mapped' | 'validated' | 'queued' | 'running' | 'completed' | 'failed'

/** aayaat job.Params. */
export interface ImportParams { as_of?: string; store_id?: string; notify_phone?: string }

/** aayaat job.Reconciliation — Dr vs Cr of opening balances. */
export interface ImportReconciliation { dr_total_paise: number; cr_total_paise: number; difference_paise: number; balanced: boolean }

/** aayaat job.Job. */
export interface ImportJob {
  id: string
  kind: ImportKind
  status: ImportStatus
  file_name: string
  file_size: number
  sheet?: string
  header_row: number
  mapping: Record<string, string>
  params: ImportParams
  total_rows: number
  valid_rows: number
  error_rows: number
  imported: number
  failed: number
  reconciliation?: ImportReconciliation
  error?: string
  created_by: string
  created_at: string
  updated_at: string
  started_at?: string
  finished_at?: string
}

/** aayaat mapping.Field — one target field of an import kind. */
export interface ImportField { key: string; label: string; required: boolean; example: string }

/** aayaat job.Preview (GET /v1/imports/{id}/preview). */
export interface ImportPreview {
  sheets: string[]
  sheet: string
  header_row: number
  headers: string[]
  rows: string[][]
  suggested_mapping: Record<string, string>
  mapping: Record<string, string>
  fields: ImportField[]
}

/** aayaat job.MappingInput (PUT /v1/imports/{id}/mapping). header_row 0 = auto-detect. */
export interface ImportMappingInput { sheet?: string; header_row?: number; mapping: Record<string, string>; params?: ImportParams }

/** aayaat job.RowResult. */
export interface ImportRow { row_no: number; status: 'valid' | 'invalid' | 'imported' | 'failed'; raw: Record<string, string>; errors: string[]; record?: unknown }

/** aayaat job.ValidateSummary (POST /v1/imports/{id}/validate). */
export interface ImportValidateSummary { job: ImportJob; sample_errors: ImportRow[] }

/* ======================= buddhi ======================= */

/** buddhi llm.Usage — token accounting for one call. */
export interface AiUsage { model: string; input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_write_tokens: number }

/** buddhi kpi.DayKPI. */
export interface DayKPI {
  date: string
  sales_paise: number
  taxable_paise: number
  tax_paise: number
  bills: number
  cancelled: number
  avg_bill_paise: number
  gross_margin_paise: number
  margin_base_paise: number
  gross_margin_pct: number
}

/** buddhi kpi.TopItem. */
export interface TopItem { item_key: string; name: string; sales_paise: number; qty?: number }

/** buddhi kpi.Dashboard (GET /v1/dashboard). store_id "all" = whole business. */
export interface AiDashboard {
  store_id: string
  date: string
  today: DayKPI
  yesterday: DayKPI
  sales_change_pct?: number
  last_7_days: DayKPI[]
  top_items: TopItem[]
  payment_mix_paise: Record<string, number>
  source: 'redis' | 'postgres'
  generated_at: string
}

/** POST /v1/ask → {answer, usage}. */
export interface AskResponse { answer: string; usage: AiUsage }

/** buddhi scan.DraftLine — matches aadesh CreateInput.lines. */
export interface ScanDraftLine {
  item_id?: string
  name: string
  unit?: string
  hsn?: string
  qty: number
  unit_price_paise: number
  discount_paise: number
  tax_rate_bp: number
  price_includes_tax: boolean
}

/** buddhi scan.LineMatch — how a line was matched to the catalog. */
export interface ScanLineMatch { line_no: number; billed_name: string; matched_name?: string; score: number }

/** buddhi scan.DraftBody. party_id is empty until a supplier is picked. */
export interface ScanDraft { doc_type: 'purchase'; party_id: string; supplier_invoice_no?: string; invoice_date?: string; lines: ScanDraftLine[]; notes?: string }

/** buddhi scan.Supplier. */
export interface ScanSupplier { name: string; gstin?: string; state_code?: string; gstin_valid: boolean }

/** buddhi scan.Result. */
export interface ScanResult {
  draft: ScanDraft
  supplier: ScanSupplier
  matches: ScanLineMatch[]
  confidence: number
  warnings: string[]
  computed_grand_total_paise: number
  printed_grand_total_paise: number
  model_confidence: number
}

/** POST /v1/scan/purchase-bill → {result, usage}. */
export interface ScanResponse { result: ScanResult; usage: AiUsage }

export type AnomalySeverity = 'info' | 'warning' | 'critical'

/** buddhi anomaly.Anomaly. */
export interface Anomaly {
  id: number
  kind: string
  severity: AnomalySeverity
  store_id?: string
  subject: string
  message: string
  details: unknown
  resolved: boolean
  detected_at: string
}

/** buddhi anomaly.CashierCancels. */
export interface CashierCancels { cashier_id: string; bills: number; cancelled: number; rate_pct: number; tenant_avg_pct: number }

/** buddhi anomaly.Report (GET /v1/insights/anomalies). */
export interface AnomalyReport { since: string; anomalies: Anomaly[]; high_cancellation_cashiers: CashierCancels[] }

/** buddhi jobs.ReorderLine. */
export interface ReorderLine {
  item_id: string
  name: string
  on_hand: number
  forecast_daily_demand: number
  forecast_demand_over_lead_time: number
  suggested_qty: number
  reason: 'forecast' | 'below_reorder_level'
  preferred_supplier_id?: string
}

/** buddhi jobs.ReorderReport (GET /v1/forecast/reorder). */
export interface ReorderReport { store_id: string; lead_days: number; cover_days: number; items: ReorderLine[]; warnings: string[] }

/** buddhi store.Settings (GET/PUT /v1/ai/settings). */
export interface AiSettings { tenant_id: string; ai_enabled: boolean; monthly_token_budget?: number; owner_phone?: string; updated_at: string }

/** buddhi store.FeatureUsage. */
export interface AiFeatureUsage { feature: string; model: string; calls: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_write_tokens: number }

/** GET /v1/ai/usage. token_budget 0 = unlimited. */
export interface AiUsageReport {
  month_start: string
  tokens_used: number
  token_budget: number
  ai_enabled: boolean
  /** False when the server has no ANTHROPIC_API_KEY (AI calls answer 503). */
  ai_configured: boolean
  by_feature: AiFeatureUsage[]
}

/** Hand-off to /app/purchases/new: stored in sessionStorage under PURCHASE_DRAFT_KEY. Mirrors aadesh CreateInput. */
export const PURCHASE_DRAFT_KEY = 'maapan.purchaseDraft'
/**
 * Pre-filled purchase bill or debit note handed to /app/purchases/new through
 * sessionStorage[PURCHASE_DRAFT_KEY]. Producers: bill scan (purchase, no party),
 * re-order drafts (purchase per supplier) and return-to-vendor (purchase_return
 * against ref_invoice_id). The purchase editor accepts every field below.
 */
export interface PurchaseDraft {
  doc_type: 'purchase' | 'purchase_return'
  store_id?: string
  party_id?: string
  ref_invoice_id?: string
  supplier_invoice_no?: string
  supplier_invoice_date?: string
  lines: {
    item_id?: string
    name: string
    hsn?: string
    unit?: string
    qty: number
    unit_price_paise: number
    discount_paise?: number
    tax_rate_bp: number
    cess_rate_bp?: number
    price_includes_tax?: boolean
    tax_category?: 'taxable' | 'exempt' | 'nil_rated' | 'non_gst'
    batch_no?: string
    expiry_date?: string
  }[]
  notes?: string
}
