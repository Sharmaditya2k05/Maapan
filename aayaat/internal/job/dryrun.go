package job

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/maapan/aayaat/internal/clients"
	"github.com/maapan/aayaat/internal/mapping"
	"github.com/maapan/mool/httpx"
)

// DryRunCall is one bulk call a commit would make.
type DryRunCall struct {
	Phase    string            `json:"phase"` // main | opening_stock
	Service  string            `json:"service"`
	Path     string            `json:"path"`
	StoreID  string            `json:"store_id,omitempty"`
	Records  int               `json:"records"`
	FirstRow int               `json:"first_row"`
	LastRow  int               `json:"last_row"`
	Sample   []json.RawMessage `json:"sample"` // first records exactly as they would be sent
}

// UnresolvedRef is a party / item reference that no import of this tenant
// carries; the record must already exist downstream or the row will fail.
type UnresolvedRef struct {
	Ref   string `json:"ref"`
	Rows  []int  `json:"rows"` // first rows that use it
	Count int    `json:"count"`
}

// DryRun is what a commit would do, computed without calling bahi or bhandar.
type DryRun struct {
	Job            *Job            `json:"job"`
	Calls          []DryRunCall    `json:"calls"`
	WouldSend      int             `json:"would_send"`      // rows that would be sent
	ZeroRows       int             `json:"zero_rows"`       // rows that would be skipped (bills netting to zero)
	Unresolved     []UnresolvedRef `json:"unresolved"`      // references not found in any aayaat import
	NotSentFields  []string        `json:"not_sent_fields"` // validated fields the downstream does not accept yet
	Notes          []string        `json:"notes"`           // things to know before committing
	Reconciliation *Reconciliation `json:"reconciliation,omitempty"`
}

const dryRunSample = 3

// DryRunCommit plans the commit of a validated job: the exact calls, a sample
// of each payload, references that will not resolve, and fields that will be
// dropped. Nothing is sent and the job does not change.
func (s *Service) DryRunCommit(ctx context.Context, tenantID, id string) (*DryRun, error) {
	j, err := s.Get(ctx, tenantID, id)
	if err != nil {
		return nil, err
	}
	if j.Status != StatusValidated && j.Status != StatusFailed {
		return nil, httpx.Conflict("job is %s; validate it before a dry run", j.Status)
	}
	if j.Params.AsOf == "" {
		j.Params.AsOf = defaultAsOf(s.Now())
	}
	out := &DryRun{Job: j, Calls: []DryRunCall{}, Unresolved: []UnresolvedRef{}, NotSentFields: []string{}, Notes: []string{},
		Reconciliation: j.Recon}
	dropped := map[string]bool{}
	refs := map[string][]int{}
	openingByStore := map[string]*DryRunCall{}
	var openingOrder []string
	err = s.plan(ctx, j, func(b *batch) error {
		lo, hi := b.span()
		call := DryRunCall{Phase: b.phase, Service: serviceOf(b.path), Path: b.path, StoreID: b.storeID, Records: len(b.recs),
			FirstRow: lo, LastRow: hi}
		call.Sample = b.recs[:min(dryRunSample, len(b.recs))]
		out.Calls = append(out.Calls, call)
		out.WouldSend += len(b.rows())
		for i, rec := range b.orig {
			droppedFields(rec, b.recs[i], dropped)
			if ref := referencedRef(j.Kind, rec); ref != "" && len(refs[ref]) < 5 {
				refs[ref] = append(refs[ref], b.groups[i][0])
			} else if ref != "" {
				refs[ref] = append(refs[ref], -1) // count only
			}
			if j.Kind == mapping.KindItems {
				var it struct {
					OpeningQty float64 `json:"opening_qty"`
					StoreID    string  `json:"store_id"`
				}
				_ = json.Unmarshal(rec, &it)
				if it.OpeningQty > 0 {
					st := it.StoreID
					if st == "" {
						st = j.Params.StoreID
					}
					c, ok := openingByStore[st]
					if !ok {
						c = &DryRunCall{Phase: "opening_stock", Service: "bhandar", Path: clients.PathOpeningStock, StoreID: st, FirstRow: b.groups[i][0]}
						openingByStore[st] = c
						openingOrder = append(openingOrder, st)
					}
					c.Records++
					c.LastRow = b.groups[i][0]
				}
			}
		}
		return nil
	}, func(rows []int) error {
		out.ZeroRows += len(rows)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, st := range openingOrder {
		out.Calls = append(out.Calls, *openingByStore[st])
	}
	for f := range dropped {
		out.NotSentFields = append(out.NotSentFields, f)
	}
	sort.Strings(out.NotSentFields)
	if len(out.NotSentFields) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%v are validated but not sent: the downstream does not accept them yet (enable BAHI_EXTRAS / BHANDAR_EXTRAS once it does)", out.NotSentFields))
	}
	if err := s.unresolved(ctx, j, refs, out); err != nil {
		return nil, err
	}
	out.Notes = append(out.Notes, s.kindNotes(ctx, j)...)
	return out, nil
}

func serviceOf(path string) string {
	switch path {
	case clients.PathItems, clients.PathOpeningStock:
		return "bhandar"
	}
	return "bahi"
}

// droppedFields notes fields with a value in orig that Shape removed.
func droppedFields(orig, sent json.RawMessage, into map[string]bool) {
	var a, b map[string]json.RawMessage
	if json.Unmarshal(orig, &a) != nil || json.Unmarshal(sent, &b) != nil {
		return
	}
	for k, v := range a {
		if _, kept := b[k]; kept || isZeroJSON(v) || aayaatOnly[k] {
			continue
		}
		into[k] = true
	}
}

// aayaatOnly are record fields used by aayaat itself, never meant for the downstream.
var aayaatOnly = map[string]bool{"store_id": true, "ledger": true, "opening_qty": true, "opening_cost_paise": true}

func isZeroJSON(v json.RawMessage) bool {
	switch string(v) {
	case `""`, `0`, `false`, `null`, `[]`, `{}`:
		return true
	}
	return false
}

// referencedRef is the party / item a row points at (for kinds that point).
func referencedRef(kind string, rec json.RawMessage) string {
	var r struct {
		PartyExternalRef string `json:"party_external_ref"`
		ItemExternalRef  string `json:"item_external_ref"`
	}
	_ = json.Unmarshal(rec, &r)
	switch kind {
	case mapping.KindOpeningStock:
		return r.ItemExternalRef
	case mapping.KindOpeningBalances, mapping.KindOpenBills, mapping.KindInvoices:
		return r.PartyExternalRef
	}
	return ""
}

// unresolved lists refs that no parties / items job of the tenant carries.
func (s *Service) unresolved(ctx context.Context, j *Job, refs map[string][]int, out *DryRun) error {
	if len(refs) == 0 {
		return nil
	}
	master := mapping.KindParties
	if j.Kind == mapping.KindOpeningStock {
		master = mapping.KindItems
	}
	all := make([]string, 0, len(refs))
	for r := range refs {
		all = append(all, r)
	}
	rows, err := s.DB.Query(ctx, `SELECT DISTINCT r.record->>'external_ref' FROM import_rows r
		JOIN import_jobs j ON j.id = r.job_id
		WHERE r.tenant_id=$1 AND j.kind=$2 AND r.status IN ('valid','imported') AND j.status <> 'rolled_back'
		  AND r.record->>'external_ref' = ANY($3)`, j.tenantID, master, all)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for rows.Next() {
		var ref string
		if err := rows.Scan(&ref); err != nil {
			rows.Close()
			return err
		}
		known[ref] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, ref := range all {
		if known[ref] {
			continue
		}
		u := UnresolvedRef{Ref: ref, Count: len(refs[ref])}
		for _, n := range refs[ref] {
			if n > 0 {
				u.Rows = append(u.Rows, n)
			}
		}
		out.Unresolved = append(out.Unresolved, u)
	}
	sort.Slice(out.Unresolved, func(a, b int) bool { return out.Unresolved[a].Rows[0] < out.Unresolved[b].Rows[0] })
	if len(out.Unresolved) > 0 {
		what := "party"
		if master == mapping.KindItems {
			what = "item"
		}
		out.Notes = append(out.Notes, fmt.Sprintf("%d %s references are in no aayaat %s import; they must already exist in %s or those rows will fail",
			len(out.Unresolved), what, master, serviceOfKind(master)))
	}
	return nil
}

func serviceOfKind(kind string) string {
	if kind == mapping.KindItems {
		return "bhandar"
	}
	return "bahi"
}

// kindNotes are kind-specific warnings worth reading before a commit.
func (s *Service) kindNotes(ctx context.Context, j *Job) []string {
	var notes []string
	switch j.Kind {
	case mapping.KindOpenBills:
		if j.Params.BillsMode == BillsPartyTotals {
			notes = append(notes, "party_totals: each party's bills are summed into one opening balance (bill-wise aging starts from the opening date)")
		} else {
			notes = append(notes, "bill_wise needs bahi POST "+clients.PathOpenBills+" (not built yet); use bills_mode=party_totals with today's bahi")
		}
		var n int
		_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM import_rows r JOIN import_jobs j ON j.id=r.job_id
			WHERE r.tenant_id=$1 AND j.kind='parties' AND r.status IN ('valid','imported') AND j.status <> 'rolled_back'
			  AND (r.record->>'opening_balance_paise')::bigint > 0`, j.tenantID).Scan(&n)
		if n > 0 {
			notes = append(notes, fmt.Sprintf("%d imported parties already carry an opening balance; importing their open bills too would count the outstanding twice — re-import the party master without opening balances", n))
		}
	case mapping.KindInvoices:
		notes = append(notes, "invoice history needs bahi POST "+clients.PathInvoiceHistory+" (not built yet); invoices dated before "+j.Params.AsOf+
			" are history only, later ones would be posted; stock is never moved")
	case mapping.KindOpeningBalances:
		if r := j.Recon; r != nil && !r.Balanced {
			notes = append(notes, fmt.Sprintf("Dr and Cr differ by ₹%.2f; bahi books the difference to Opening Balance Difference", float64(abs64(r.DifferencePaise))/100))
		}
	}
	return notes
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
