// Package job runs import jobs: upload → preview/mapping → validate → (dry run)
// → commit → (rollback).
package job

import (
	"strings"
	"time"

	"github.com/maapan/aayaat/internal/mapping"
	"github.com/maapan/mool/httpx"
)

// Job statuses.
const (
	StatusUploaded   = "uploaded"
	StatusMapped     = "mapped"
	StatusValidated  = "validated"
	StatusQueued     = "queued" // handed to the aayaat-worker Cloud Run Job
	StatusRunning    = "running"
	StatusCompleted  = "completed"
	StatusFailed     = "failed"
	StatusRolledBack = "rolled_back"
)

// Row statuses.
const (
	RowValid      = "valid"
	RowInvalid    = "invalid"
	RowImported   = "imported"
	RowFailed     = "failed"
	RowSkipped    = "skipped" // nothing to import: zero quantity, zero balance, settled bill
	RowRolledBack = "rolled_back"
)

// Sources an import can come from (informational; Tally XML is detected).
var Sources = []string{"marg", "tally", "busy", "vyapar", "excel"}

// Bills modes for open_bills.
const (
	BillsBillWise    = "bill_wise"    // one open bill per row → bahi bill-wise outstanding
	BillsPartyTotals = "party_totals" // net per party → one opening balance per party (works with today's bahi)
)

// Params are per-job options.
type Params struct {
	AsOf        string            `json:"as_of,omitempty"`       // opening date (YYYY-MM-DD), default 1 April of the FY
	StoreID     string            `json:"store_id,omitempty"`    // default store for stock rows / items with opening qty
	StoreMap    map[string]string `json:"store_map,omitempty"`   // store / godown text in the file → store id
	AccountMap  map[string]string `json:"account_map,omitempty"` // ledger or group name → bahi account code
	DocType     string            `json:"doc_type,omitempty"`    // invoices: sale | purchase | sale_return | purchase_return
	Side        string            `json:"side,omitempty"`        // open_bills: dr (receivable) | cr (payable) when the file does not say
	BillsMode   string            `json:"bills_mode,omitempty"`  // open_bills: bill_wise (default) | party_totals
	Source      string            `json:"source,omitempty"`      // marg | tally | busy | vyapar | excel
	NotifyPhone string            `json:"notify_phone,omitempty"`
}

// Job is an import job.
type Job struct {
	ID           string            `json:"id"`
	Kind         string            `json:"kind"`
	Status       string            `json:"status"`
	Source       string            `json:"source,omitempty"`
	FileName     string            `json:"file_name"`
	FileSize     int64             `json:"file_size"`
	Sheet        string            `json:"sheet,omitempty"`
	HeaderRow    int               `json:"header_row"`
	Mapping      map[string]string `json:"mapping"`
	Params       Params            `json:"params"`
	TemplateID   *string           `json:"template_id,omitempty"`
	TotalRows    int               `json:"total_rows"`
	ValidRows    int               `json:"valid_rows"`
	ErrorRows    int               `json:"error_rows"`
	SkippedRows  int               `json:"skipped_rows"`
	WarningRows  int               `json:"warning_rows"`
	Imported     int               `json:"imported"`
	Failed       int               `json:"failed"`
	RolledBack   int               `json:"rolled_back"`
	Recon        *Reconciliation   `json:"reconciliation,omitempty"`
	Error        *string           `json:"error,omitempty"`
	CreatedBy    string            `json:"created_by"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
	StartedAt    *time.Time        `json:"started_at,omitempty"`
	FinishedAt   *time.Time        `json:"finished_at,omitempty"`
	RolledBackAt *time.Time        `json:"rolled_back_at,omitempty"`
	fileKey      string
	contentType  string
	tenantID     string
}

// Reconciliation compares total debits and credits of opening balances. In a
// complete trial balance they are equal; a difference usually means a missing
// ledger (capital, stock, cash) that must be added before go-live.
type Reconciliation struct {
	DrTotalPaise    int64 `json:"dr_total_paise"`
	CrTotalPaise    int64 `json:"cr_total_paise"`
	DifferencePaise int64 `json:"difference_paise"` // Dr − Cr
	Balanced        bool  `json:"balanced"`
}

func reconciliation(kind string, dr, cr int64) *Reconciliation {
	switch kind {
	case mapping.KindParties, mapping.KindOpeningBalances, mapping.KindOpenBills:
		return &Reconciliation{DrTotalPaise: dr, CrTotalPaise: cr, DifferencePaise: dr - cr, Balanced: dr == cr}
	}
	return nil
}

// MappingInput is the body of PUT /v1/imports/{id}/mapping.
type MappingInput struct {
	Sheet      string            `json:"sheet,omitempty"`
	HeaderRow  int               `json:"header_row,omitempty"`
	Mapping    map[string]string `json:"mapping"`
	Params     *Params           `json:"params,omitempty"`
	TemplateID string            `json:"template_id,omitempty"` // apply a saved mapping (fields left empty here come from it)
	SaveAs     string            `json:"save_as,omitempty"`     // also save this mapping as a template with this name
}

// ValidateFor checks the mapping against kind's fields.
func (m *MappingInput) ValidateFor(kind string) error {
	if err := checkMapping(kind, m.Mapping); err != nil {
		return err
	}
	if m.HeaderRow < 0 {
		return httpx.Validation("header_row must be >= 1 (0 = auto)")
	}
	if len(m.SaveAs) > 80 {
		return httpx.Validation("save_as: at most 80 characters")
	}
	if m.Params != nil {
		return m.Params.validate()
	}
	return nil
}

func checkMapping(kind string, m map[string]string) error {
	known := map[string]bool{}
	for _, f := range mapping.Fields[kind] {
		known[f.Key] = true
	}
	for k := range m {
		if !known[k] {
			return httpx.Validation("unknown field %q for %s", k, kind)
		}
	}
	if missing := mapping.MissingRequired(kind, m); len(missing) > 0 {
		return httpx.Validation("map the required fields: %v", missing)
	}
	return nil
}

func (p *Params) validate() error {
	if p.AsOf != "" {
		if _, err := time.Parse(time.DateOnly, p.AsOf); err != nil {
			return httpx.Validation("as_of must be YYYY-MM-DD")
		}
	}
	switch p.DocType {
	case "", "sale", "purchase", "sale_return", "purchase_return":
	default:
		return httpx.Validation("doc_type must be sale, purchase, sale_return or purchase_return")
	}
	switch p.Side {
	case "", "dr", "cr":
	default:
		return httpx.Validation(`side must be "dr" (receivable) or "cr" (payable)`)
	}
	switch p.BillsMode {
	case "", BillsBillWise, BillsPartyTotals:
	default:
		return httpx.Validation("bills_mode must be bill_wise or party_totals")
	}
	if p.Source != "" {
		ok := false
		for _, s := range Sources {
			ok = ok || s == p.Source
		}
		if !ok {
			return httpx.Validation("source must be one of %v", Sources)
		}
	}
	for k, code := range p.AccountMap {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(code) == "" {
			return httpx.Validation("account_map entries need a ledger name and an account code")
		}
	}
	for k, id := range p.StoreMap {
		if strings.TrimSpace(k) == "" || strings.TrimSpace(id) == "" {
			return httpx.Validation("store_map entries need a name and a store id")
		}
	}
	return nil
}

// storeIDs lists every store a job's params can write to.
func (p *Params) storeIDs() []string {
	var out []string
	if p.StoreID != "" {
		out = append(out, p.StoreID)
	}
	for _, id := range p.StoreMap {
		out = append(out, id)
	}
	return out
}

// reusable keeps the params worth saving with a mapping template.
func (p Params) reusable() Params {
	return Params{StoreID: p.StoreID, StoreMap: p.StoreMap, AccountMap: p.AccountMap, DocType: p.DocType, Side: p.Side,
		BillsMode: p.BillsMode, Source: p.Source}
}

// merge fills empty fields of p from t.
func (p Params) merge(t Params) Params {
	if p.StoreID == "" {
		p.StoreID = t.StoreID
	}
	if len(p.StoreMap) == 0 {
		p.StoreMap = t.StoreMap
	}
	if len(p.AccountMap) == 0 {
		p.AccountMap = t.AccountMap
	}
	if p.DocType == "" {
		p.DocType = t.DocType
	}
	if p.Side == "" {
		p.Side = t.Side
	}
	if p.BillsMode == "" {
		p.BillsMode = t.BillsMode
	}
	if p.Source == "" {
		p.Source = t.Source
	}
	return p
}

// IST is India Standard Time.
var IST = time.FixedZone("IST", 5*3600+1800)

// defaultAsOf is 1 April of the current financial year: migrations normally
// bring balances as at the start of the year being migrated into.
func defaultAsOf(now time.Time) string {
	n := now.In(IST)
	y := n.Year()
	if n.Month() < time.April {
		y--
	}
	return time.Date(y, time.April, 1, 0, 0, 0, 0, IST).Format(time.DateOnly)
}
