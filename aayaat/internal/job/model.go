// Package job runs import jobs: upload → preview/mapping → validate → commit.
package job

import (
	"time"

	"github.com/taraju/aayaat/internal/mapping"
	"github.com/taraju/mool/httpx"
)

// Job statuses.
const (
	StatusUploaded  = "uploaded"
	StatusMapped    = "mapped"
	StatusValidated = "validated"
	StatusQueued    = "queued" // handed to the aayaat-worker Cloud Run Job
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

// Params are per-job options.
type Params struct {
	AsOf        string `json:"as_of,omitempty"`    // opening balances / stock date (YYYY-MM-DD)
	StoreID     string `json:"store_id,omitempty"` // opening_stock target store
	NotifyPhone string `json:"notify_phone,omitempty"`
}

// Job is an import job.
type Job struct {
	ID          string            `json:"id"`
	Kind        string            `json:"kind"`
	Status      string            `json:"status"`
	FileName    string            `json:"file_name"`
	FileSize    int64             `json:"file_size"`
	Sheet       string            `json:"sheet,omitempty"`
	HeaderRow   int               `json:"header_row"`
	Mapping     map[string]string `json:"mapping"`
	Params      Params            `json:"params"`
	TotalRows   int               `json:"total_rows"`
	ValidRows   int               `json:"valid_rows"`
	ErrorRows   int               `json:"error_rows"`
	Imported    int               `json:"imported"`
	Failed      int               `json:"failed"`
	Recon       *Reconciliation   `json:"reconciliation,omitempty"`
	Error       *string           `json:"error,omitempty"`
	CreatedBy   string            `json:"created_by"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
	StartedAt   *time.Time        `json:"started_at,omitempty"`
	FinishedAt  *time.Time        `json:"finished_at,omitempty"`
	fileKey     string
	contentType string
	tenantID    string
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
	if kind != mapping.KindParties && kind != mapping.KindOpeningBalances {
		return nil
	}
	return &Reconciliation{DrTotalPaise: dr, CrTotalPaise: cr, DifferencePaise: dr - cr, Balanced: dr == cr}
}

// MappingInput is the body of PUT /v1/imports/{id}/mapping.
type MappingInput struct {
	Sheet     string            `json:"sheet,omitempty"`
	HeaderRow int               `json:"header_row,omitempty"`
	Mapping   map[string]string `json:"mapping"`
	Params    *Params           `json:"params,omitempty"`
}

// ValidateFor checks the mapping against kind's fields.
func (m *MappingInput) ValidateFor(kind string) error {
	known := map[string]bool{}
	for _, f := range mapping.Fields[kind] {
		known[f.Key] = true
	}
	for k := range m.Mapping {
		if !known[k] {
			return httpx.Validation("unknown field %q for %s", k, kind)
		}
	}
	if missing := mapping.MissingRequired(kind, m.Mapping); len(missing) > 0 {
		return httpx.Validation("map the required fields: %v", missing)
	}
	if m.HeaderRow < 0 {
		return httpx.Validation("header_row must be >= 1 (0 = auto)")
	}
	if m.Params != nil {
		return m.Params.validate()
	}
	return nil
}

func (p *Params) validate() error {
	if p.AsOf != "" {
		if _, err := time.Parse(time.DateOnly, p.AsOf); err != nil {
			return httpx.Validation("as_of must be YYYY-MM-DD")
		}
	}
	return nil
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
