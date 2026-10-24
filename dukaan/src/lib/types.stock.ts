/**
 * bhandar (inventory) and settings shapes, named after the Go structs.
 * Money = paise, quantities ≤ 3 decimals, tax rates = basis points.
 *
 * Every *Input type lists exactly the fields its Go struct accepts: the servers decode
 * with DisallowUnknownFields, so an extra key (an `id`, a `status`) is a 400.
 */
import type { DocType, Item, StockBalance } from './types'
import type { Role } from './rbac'

/* ---------------- item master ---------------- */

/** bhandar Unit (GET/POST /v1/units). */
export interface Unit { code: string; name: string }

/** bhandar AltUnit: 1 <unit> = factor × base unit. */
export interface AltUnit { unit: string; factor: number }

export type TaxCategory = 'taxable' | 'exempt' | 'nil_rated' | 'non_gst'
export const TAX_CATEGORY_LABEL: Record<TaxCategory, string> = { taxable: 'Taxable', exempt: 'Exempt', nil_rated: 'Nil rated', non_gst: 'Non-GST' }

/** bhandar Item with every column (types.ts Item is the subset the POS needs). */
export interface ItemFull extends Item {
  plu?: string
  cess_per_unit_paise?: number
  tax_category: TaxCategory
  preferred_supplier_id?: string
  external_ref?: string
  alt_units: AltUnit[]
  created_at: string
  updated_at: string
}

/** bhandar ItemInput (POST /v1/items, PUT /v1/items/{id}). */
export interface ItemInput {
  name: string
  sku?: string
  barcode?: string
  plu?: string
  hsn?: string
  unit?: string
  category?: string
  brand?: string
  sale_price_paise: number
  mrp_paise: number
  purchase_price_paise: number
  tax_rate_bp: number
  cess_rate_bp?: number
  cess_per_unit_paise?: number
  tax_category?: TaxCategory
  price_includes_tax?: boolean
  track_batches?: boolean
  reorder_level?: number
  reorder_qty?: number
  preferred_supplier_id?: string
  external_ref?: string
  alt_units?: AltUnit[]
  generate_barcode?: boolean
}

/** bhandar ReorderInput (PUT /v1/items/{id}/reorder). */
export interface ReorderInput { reorder_level: number; reorder_qty: number; preferred_supplier_id?: string }

/** bhandar Batch (GET /v1/items/{id}/batches → {batches}). */
export interface Batch { id: string; item_id: string; batch_no: string; mfg_date?: string; expiry_date?: string; mrp_paise?: number | null; qty: number }

/** bhandar Label (POST /v1/labels → {labels}). */
export interface Label { item_id: string; name: string; barcode: string; mrp_paise?: number; sale_price_paise?: number; copies: number; svg: string }
export interface LabelRequest { items: { item_id: string; copies: number }[]; show_price: boolean; module?: number; height?: number }

/* ---------------- stock ledger ---------------- */

/** bhandar Balance (GET /v1/stock) with the columns types.ts StockBalance leaves out. */
export interface Balance extends StockBalance { unit: string; batch_id?: string; reorder_level?: number; last_in_at?: string; last_out_at?: string }

export type MovementReason = 'sale' | 'purchase' | 'sale_return' | 'purchase_return' | 'transfer_in' | 'transfer_out' | 'adjustment' | 'opening' | 'production_consume' | 'production_output' | 'expiry_writeoff'

/** bhandar Movement (item ledger rows, adjustment results). */
export interface Movement {
  id: string
  store_id: string
  item_id: string
  item_name?: string
  unit?: string
  batch_id?: string
  batch_no?: string
  qty: number
  unit_cost_paise: number
  value_paise: number
  reason: MovementReason
  source: string
  source_id: string
  line_no: number
  note?: string
  created_by: string
  created_at: string
  running_qty?: number
}

export type AdjustmentReason = 'adjustment' | 'expiry_writeoff'
/** bhandar AdjustmentLine. */
export interface AdjustmentLine { item_id: string; batch_no?: string; expiry_date?: string; qty: number; unit?: string; unit_cost_paise?: number }
/** bhandar AdjustmentInput (POST /v1/stock/adjustments). */
export interface AdjustmentInput { store_id?: string; reason: AdjustmentReason; note: string; lines: AdjustmentLine[] }
/** bhandar Adjustment (response). */
export interface Adjustment { id: string; store_id: string; reason: AdjustmentReason; note: string; movements: Movement[] }

/* ---------------- transfers ---------------- */

export type TransferStatus = 'draft' | 'dispatched' | 'received' | 'cancelled'
export type TransferAction = 'dispatch' | 'receive' | 'cancel'

export interface TransferLine { line_no: number; item_id: string; item_name?: string; batch_id?: string; batch_no?: string; qty: number; unit_cost_paise: number }
export interface Transfer {
  id: string
  from_store_id: string
  to_store_id: string
  status: TransferStatus
  notes?: string
  lines: TransferLine[]
  created_by: string
  created_at: string
  dispatched_at?: string
  received_at?: string
  cancelled_at?: string
}
export interface TransferLineInput { item_id: string; batch_no?: string; qty: number; unit?: string }
export interface TransferInput { from_store_id: string; to_store_id: string; notes?: string; lines: TransferLineInput[] }

/* ---------------- re-order, expiry, analytics ---------------- */

/** bhandar Suggestion (GET /v1/reorder/suggestions → {store_id, items}). */
export interface Suggestion {
  item_id: string
  name: string
  sku?: string
  hsn?: string
  unit: string
  on_hand: number
  reorder_level: number
  reorder_qty: number
  suggested_qty: number
  preferred_supplier_id?: string
  purchase_price_paise: number
  tax_rate_bp: number
  cess_rate_bp: number
  tax_category: TaxCategory
}

/** One line of an aadesh CreateInput as bhandar drafts it. */
export interface PurchaseDraftLine {
  item_id: string
  name: string
  hsn?: string
  unit?: string
  batch_no?: string
  qty: number
  unit_price_paise: number
  discount_paise?: number
  tax_rate_bp: number
  cess_rate_bp?: number
  price_includes_tax?: boolean
  tax_category?: TaxCategory
}

/** The aadesh body handed to /app/purchases/new through sessionStorage (see lib/stock/handoff.ts). */
export interface PurchaseDraftBody {
  doc_type: 'purchase' | 'purchase_return'
  store_id?: string
  party_id?: string
  ref_invoice_id?: string
  lines: PurchaseDraftLine[]
  notes?: string
}

/** bhandar PurchaseDraft (POST /v1/reorder/purchase-draft → {drafts}). supplier_id "" = no preferred supplier. */
export interface PurchaseDraft { supplier_id: string; items: number; value_paise: number; body: PurchaseDraftBody }
export interface PurchaseDraftInput { store_id?: string; supplier_id?: string; item_ids?: string[] }

/** bhandar ExpiringBatch (GET /v1/batches/expiring → {days, items}). days_left < 0 = expired. */
export interface ExpiringBatch {
  batch_id: string
  item_id: string
  item_name: string
  unit: string
  store_id: string
  batch_no: string
  expiry_date: string
  days_left: number
  qty: number
  avg_cost_paise: number
  value_paise: number
}
/** bhandar ReturnToVendorInput (POST /v1/batches/{id}/return-to-vendor → {draft}). */
export interface ReturnToVendorInput { store_id?: string; qty?: number; supplier_id?: string }

/** bhandar DeadStockRow (GET /v1/analytics/dead-stock → {days, items, total_value_paise}). */
export interface DeadStockRow { store_id: string; item_id: string; name: string; unit: string; qty: number; avg_cost_paise: number; value_paise: number; last_in_at?: string; last_out_at?: string; idle_days: number }
/** bhandar AgingRow (GET /v1/analytics/stock-aging → {buckets}). */
export interface AgingRow { bucket: '0-30' | '31-60' | '61-90' | '90+'; items: number; qty: number; value_paise: number }
/** bhandar ValuationRow / Valuation (GET /v1/analytics/valuation). */
export interface ValuationRow { item_id: string; name: string; unit: string; qty: number; avg_cost_paise: number; value_paise: number }
export interface Valuation { store_id?: string; as_of: string; items: ValuationRow[]; total_qty: number; total_value_paise: number }

/* ---------------- settings: pehchaan ---------------- */

/** pehchaan Tenant (GET /v1/tenant). Optional text comes back null when unset. */
export interface Tenant {
  id: string
  business_name: string
  gstin?: string | null
  pan?: string | null
  state_code: string
  address?: string | null
  email?: string | null
  phone?: string | null
  status: string
  created_at: string
}
/** pehchaan TenantInput (PUT /v1/tenant). */
export interface TenantInput { business_name: string; gstin?: string; state_code: string; address?: string; email?: string; phone?: string }
/** pehchaan StoreInput (POST /v1/stores, PUT /v1/stores/{id}). */
export interface StoreInput { name: string; code: string; state_code: string; gstin?: string; address?: string; phone?: string; active?: boolean }
/** pehchaan Member (GET /v1/users). */
export interface Member { id: string; phone: string; name?: string | null; email?: string | null; status: string; last_login_at?: string | null; role: Role; store_ids: string[]; active: boolean }
/** pehchaan InviteInput (POST /v1/users). */
export interface InviteInput { phone: string; name: string; role: Role; store_ids: string[] }
/** pehchaan MemberUpdate (PATCH /v1/users/{id}). */
export interface MemberUpdate { name?: string; role?: Role; store_ids?: string[]; active?: boolean }

/* ---------------- settings: aadesh, bahi, lenden, kar, buddhi ---------------- */

/** aadesh NumberingSetting (PUT /v1/settings/numbering; rows of GET …stores). */
export interface NumberingSetting { store_id: string; doc_type: DocType; prefix: string }
/** aadesh GET /v1/settings/numbering. */
export interface NumberingResponse { default_prefixes: Record<DocType, string>; stores: NumberingSetting[] }

/** bahi PeriodLock (GET/PUT /v1/settings/period-lock). locked_until "" = books open. */
export interface PeriodLock { locked_until: string; locked_by?: string; locked_at?: string | null }

export type GatewayName = 'razorpay' | 'cashfree'
/** lenden creds.Summary (GET /v1/settings/gateways → {items, configurable}). */
export interface GatewaySummary { gateway: GatewayName; key_id_masked: string; env?: string; has_webhook_secret: boolean; updated_by?: string; updated_at: string }
export interface GatewaySettingsResponse { items: GatewaySummary[]; configurable: GatewayName[] | null }
/** lenden GatewayCredentialInput (PUT /v1/settings/gateways/{gateway}). */
export interface GatewayCredentialInput { key_id: string; key_secret: string; webhook_secret?: string; env?: string }
/** lenden GET /v1/gateways. */
export interface GatewayAvailability { default: string; available: string[] | null }

/** kar Settings (GET /v1/settings). */
export interface TaxSettings { tenant_id: string; state_code: string; gstin: string; composition: boolean; einvoice_enabled: boolean; eway_threshold_paise: number; b2cl_threshold_paise: number; updated_at: string }
/** kar SettingsUpdate (PUT /v1/settings; only sent fields change). */
export interface TaxSettingsUpdate { state_code?: string; gstin?: string; composition?: boolean; einvoice_enabled?: boolean; eway_threshold_paise?: number; b2cl_threshold_paise?: number }

/** buddhi settingsInput (PUT /v1/ai/settings). null = platform default. Read shapes (AiSettings, AiUsageReport) live in types.more.ts. */
export interface AiSettingsInput { ai_enabled: boolean; monthly_token_budget: number | null; owner_phone: string | null }
