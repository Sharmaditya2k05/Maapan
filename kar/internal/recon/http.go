package recon

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/httpx"
)

// MaxStatementBytes caps an uploaded 2B file (a large buyer's month is a few MB).
const MaxStatementBytes = 32 << 20

// TwoBFetcher downloads a GSTIN's GSTR-2B JSON from the portal (the GSP
// client satisfies it once a GSP is configured).
type TwoBFetcher interface {
	FetchGSTR2B(ctx context.Context, gstin, period string) ([]byte, error)
}

// PurchaseLoader returns posted purchase bills and purchase returns of our
// gstin dated from..to (YYYY-MM-DD).
type PurchaseLoader func(ctx context.Context, gstin, from, to string) ([]Purchase, error)

// Service serves the GSTR-2B and IMS routes.
type Service struct {
	DB        *pgxpool.Pool
	Purchases PurchaseLoader
	Fetcher   TwoBFetcher // nil until a GSP is configured
	// ResolveGSTIN checks ?gstin= against the tenant's registrations (default primary).
	ResolveGSTIN func(r *http.Request) (string, error)
	Log          *slog.Logger
}

// Window of purchase bills searched for a period's 2B documents: a bill can
// be booked months before or after the supplier reports it.
const (
	windowBeforeDays = 180
	windowAfterDays  = 62
)

func (s *Service) store() Store { return Store{DB: s.DB} }

func (s *Service) target(r *http.Request) (gstin, period, from, to string, err error) {
	if gstin, err = s.ResolveGSTIN(r); err != nil {
		return
	}
	period = httpx.Query(r, "period")
	if from, to, err = ParsePeriod(period); err != nil {
		err = httpx.Validation("%s", err.Error())
	}
	return
}

// Import handles POST /v1/returns/gstr2b/import?gstin=&period= with the portal's 2B JSON as the body.
func (s *Service) Import(w http.ResponseWriter, r *http.Request) error {
	gstin, period, _, _, err := s.target(r)
	if err != nil {
		return err
	}
	if r.Body == nil {
		return httpx.BadRequest("request body is required (the GSTR-2B JSON)")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, MaxStatementBytes+1))
	if err != nil {
		return httpx.BadRequest("could not read the body")
	}
	if len(raw) > MaxStatementBytes {
		return httpx.Validation("2B file is larger than %d MB", MaxStatementBytes>>20)
	}
	return s.importRaw(w, r, gstin, period, "upload", raw)
}

// Fetch handles POST /v1/returns/gstr2b/fetch?gstin=&period= through the GSP.
func (s *Service) Fetch(w http.ResponseWriter, r *http.Request) error {
	gstin, period, _, _, err := s.target(r)
	if err != nil {
		return err
	}
	if s.Fetcher == nil {
		return httpx.Unavailable("GSTR-2B download needs a GST Suvidha Provider (set GSP_PROVIDER); until then download the 2B JSON from the GST portal and upload it to POST /v1/returns/gstr2b/import")
	}
	raw, err := s.Fetcher.FetchGSTR2B(r.Context(), gstin, period)
	if err != nil {
		return err
	}
	return s.importRaw(w, r, gstin, period, "gsp", raw)
}

func (s *Service) importRaw(w http.ResponseWriter, r *http.Request, gstin, period, source string, raw []byte) error {
	raw = bytes.TrimPrefix(bytes.TrimSpace(raw), []byte("\xef\xbb\xbf"))
	if len(raw) == 0 {
		return httpx.BadRequest("request body is required (the GSTR-2B JSON)")
	}
	st, err := ParseGSTR2B(raw)
	if err != nil {
		return httpx.Validation("%s", err.Error())
	}
	if st.GSTIN != "" && st.GSTIN != gstin {
		return httpx.Validation("this 2B file belongs to %s, not %s", st.GSTIN, gstin)
	}
	if st.Period != "" && st.Period != period {
		return httpx.Validation("this 2B file is for period %s, not %s", st.Period, period)
	}
	p := auth.FromContext(r.Context())
	if source == "gsp" && len(st.Docs) == 0 {
		// A simulator or a GSP hiccup returns an empty statement; never let it
		// wipe documents (and their reconciliation) already imported.
		if prev, _, err := s.store().Statement(r.Context(), p.TenantID, gstin, period); err != nil {
			return err
		} else if prev != nil && prev.DocCount > 0 {
			return httpx.Conflict("the GSP returned an empty GSTR-2B for %s %s; the existing import with %d documents was kept", gstin, period, prev.DocCount)
		}
	}
	imp, err := s.store().ReplaceImport(r.Context(), p.TenantID, gstin, period, source, p.UserID(), raw, st)
	if err != nil {
		return err
	}
	return httpx.Created(w, map[string]any{"gstin": gstin, "period": period, "import": imp})
}

// Reconciliation is the GET /v1/returns/gstr2b response.
type Reconciliation struct {
	GSTIN   string         `json:"gstin"`
	Period  string         `json:"period"`
	Import  *Import        `json:"import"`
	Counts  map[string]int `json:"counts"`
	ITC     ITCSummary     `json:"itc"`
	Items   []Result       `json:"items"`
	Warning string         `json:"warning,omitempty"`
}

// Reconcile matches the stored 2B of gstin+period with the books.
func (s *Service) Reconcile(ctx context.Context, tenantID, gstin, period, from, to string) (Reconciliation, map[string]Action, error) {
	rec := Reconciliation{GSTIN: gstin, Period: period, Items: []Result{}}
	st := s.store()
	imp, docs, err := st.Statement(ctx, tenantID, gstin, period)
	if err != nil {
		return rec, nil, err
	}
	rec.Import = imp
	actions, err := st.Actions(ctx, tenantID, gstin, docs)
	if err != nil {
		return rec, nil, err
	}
	elsewhere, err := st.ReportedElsewhere(ctx, tenantID, gstin, period)
	if err != nil {
		return rec, nil, err
	}
	f, _ := time.Parse(time.DateOnly, from)
	t, _ := time.Parse(time.DateOnly, to)
	purchases, err := s.Purchases(ctx, gstin, f.AddDate(0, 0, -windowBeforeDays).Format(time.DateOnly),
		t.AddDate(0, 0, windowAfterDays).Format(time.DateOnly))
	if err != nil {
		return rec, nil, err
	}
	rec.Items = Match(MatchInput{Docs: docs, Purchases: purchases, From: from, To: to, ReportedElsewhere: elsewhere})
	for i := range rec.Items {
		if rec.Items[i].DocKey == "" {
			continue
		}
		if a, ok := actions[rec.Items[i].DocKey]; ok {
			rec.Items[i].IMS = &a
		}
	}
	rec.Counts = Counts(rec.Items)
	rec.ITC = SummariseITC(docs, actions)
	if imp == nil {
		rec.Warning = "no GSTR-2B imported for this period yet; every purchase bill from a registered supplier shows as missing_in_2b"
	}
	return rec, actions, nil
}

// List handles GET /v1/returns/gstr2b?gstin=&period=[&status=].
func (s *Service) List(w http.ResponseWriter, r *http.Request) error {
	gstin, period, from, to, err := s.target(r)
	if err != nil {
		return err
	}
	rec, _, err := s.Reconcile(r.Context(), auth.FromContext(r.Context()).TenantID, gstin, period, from, to)
	if err != nil {
		return err
	}
	if want := httpx.Query(r, "status"); want != "" {
		kept := []Result{}
		for _, it := range rec.Items {
			if it.Status == want {
				kept = append(kept, it)
			}
		}
		rec.Items = kept
	}
	return httpx.OK(w, rec)
}

type actionInput struct {
	Action string `json:"action"`
	Remark string `json:"remark,omitempty"`
}

// SetAction handles PUT /v1/returns/ims/{doc_key}?gstin= {action, remark}.
func (s *Service) SetAction(w http.ResponseWriter, r *http.Request) error {
	gstin, err := s.ResolveGSTIN(r)
	if err != nil {
		return err
	}
	var in actionInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	in.Action = strings.ToLower(strings.TrimSpace(in.Action))
	if !ValidAction(in.Action) {
		return httpx.Validation("action must be accept, reject or pending")
	}
	if len(in.Remark) > 500 {
		return httpx.Validation("remark is limited to 500 characters")
	}
	key := r.PathValue("doc_key")
	p := auth.FromContext(r.Context())
	ok, err := s.store().DocExists(r.Context(), p.TenantID, gstin, key)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.NotFound("no GSTR-2B document %s for %s", key, gstin)
	}
	if err := s.store().SetActions(r.Context(), p.TenantID, gstin, []string{key}, in.Action, in.Remark, p.UserID()); err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"gstin": gstin, "doc_key": key, "action": in.Action, "remark": in.Remark, "actor": p.UserID()})
}

// AcceptMatched handles POST /v1/returns/ims/accept-matched?gstin=&period=:
// every matched document with no action (or pending) is accepted.
func (s *Service) AcceptMatched(w http.ResponseWriter, r *http.Request) error {
	gstin, period, from, to, err := s.target(r)
	if err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	rec, actions, err := s.Reconcile(r.Context(), p.TenantID, gstin, period, from, to)
	if err != nil {
		return err
	}
	keys := []string{}
	for _, it := range rec.Items {
		if it.Status != StatusMatched || it.DocKey == "" {
			continue
		}
		if a, ok := actions[it.DocKey]; ok && a.Action != ActionPending {
			continue
		}
		keys = append(keys, it.DocKey)
	}
	if err := s.store().SetActions(r.Context(), p.TenantID, gstin, keys, ActionAccept, "matched with books", p.UserID()); err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"gstin": gstin, "period": period, "accepted": len(keys), "doc_keys": keys})
}

// Export handles GET /v1/returns/ims/export?gstin=&period=.
func (s *Service) Export(w http.ResponseWriter, r *http.Request) error {
	gstin, period, _, _, err := s.target(r)
	if err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	imp, docs, err := s.store().Statement(r.Context(), p.TenantID, gstin, period)
	if err != nil {
		return err
	}
	if imp == nil {
		return httpx.NotFound("no GSTR-2B imported for %s %s", gstin, period)
	}
	actions, err := s.store().Actions(r.Context(), p.TenantID, gstin, docs)
	if err != nil {
		return err
	}
	f := BuildIMSFile(gstin, period, docs, actions)
	w.Header().Set("Content-Disposition", `attachment; filename="IMS_`+gstin+`_`+period+`.json"`)
	return httpx.OK(w, f)
}

// ITCFor2B returns the 2B-accepted ITC of gstin+period (ok=false when no 2B was imported).
func (s *Service) ITCFor2B(ctx context.Context, tenantID, gstin, period string) (ITCSummary, bool, error) {
	st := s.store()
	imp, docs, err := st.Statement(ctx, tenantID, gstin, period)
	if err != nil || imp == nil {
		return ITCSummary{}, false, err
	}
	actions, err := st.Actions(ctx, tenantID, gstin, docs)
	if err != nil {
		return ITCSummary{}, false, err
	}
	return SummariseITC(docs, actions), true, nil
}
