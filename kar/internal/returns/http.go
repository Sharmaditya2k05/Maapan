package returns

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/maapan/kar/internal/recon"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/db"
	"github.com/maapan/mool/httpx"
)

type target struct {
	reg      Registration
	period   string
	from, to string
}

func (s *service) target(r *http.Request) (target, error) {
	reg, err := s.registration(r.Context(), httpx.Query(r, "gstin"))
	if err != nil {
		return target{}, err
	}
	t := target{reg: reg, period: httpx.Query(r, "period")}
	if t.from, t.to, err = Period(t.period); err != nil {
		return target{}, httpx.Validation("%s", err.Error())
	}
	return t, nil
}

// BuildGSTR1 streams the period's sales and credit notes of one registration.
func (s *service) BuildGSTR1(ctx context.Context, t target) (GSTR1, error) {
	p := auth.FromContext(ctx)
	originals := map[string]Invoice{}
	b := NewGSTR1(GSTR1Config{GSTIN: t.reg.GSTIN, FP: t.period, State: t.reg.StateCode, B2CLThreshold: s.b2clThreshold(ctx, p.TenantID),
		Original: func(ctx context.Context, id string) (Invoice, error) {
			if o, ok := originals[id]; ok {
				return o, nil
			}
			o, err := s.d.Invoices.Invoice(ctx, id)
			if err == nil {
				o.Lines = nil // only the value is needed
				originals[id] = o
			}
			return o, err
		}})
	err := s.d.Invoices.EachInvoice(ctx, InvoiceQuery{GSTIN: t.reg.GSTIN, From: t.from, To: t.to,
		DocTypes: []string{"sale", "sale_return"}, IncludeLines: true}, func(inv Invoice) error { return b.Add(ctx, inv) })
	if err != nil {
		return GSTR1{}, err
	}
	g := b.Build()
	if t.reg.Composition {
		g.Warnings = append([]string{"this registration is under the composition scheme: it files CMP-08 and GSTR-4, not GSTR-1/3B"}, g.Warnings...)
	}
	return g, nil
}

func download(w http.ResponseWriter, name, contentType string) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
}

func (s *service) writeGSTR1(w http.ResponseWriter, g GSTR1, format, section string) error {
	base := "GSTR1_" + g.Gstin + "_" + g.Fp
	switch format {
	case "json":
		download(w, base+".json", "application/json; charset=utf-8")
		return httpx.OK(w, g.Portal())
	case "csv":
		if section == "" || section == "all" {
			download(w, base+".csv", "text/csv; charset=utf-8")
			return WriteGSTR1CSV(w, g)
		}
		if !slices.Contains(GSTR1Sections, section) {
			return httpx.Validation("section must be one of %s", strings.Join(GSTR1Sections, ", "))
		}
		download(w, base+"_"+section+".csv", "text/csv; charset=utf-8")
		return WriteGSTR1SectionCSV(w, g, section)
	}
	return httpx.Validation("format must be json or csv")
}

func needFile(r *http.Request) error {
	if !auth.FromContext(r.Context()).Can(auth.PermGSTFile) {
		return httpx.Forbidden("downloading a return needs the %s permission", auth.PermGSTFile)
	}
	return nil
}

func (s *service) gstr1(w http.ResponseWriter, r *http.Request) error {
	format := httpx.Query(r, "format")
	if format != "" {
		if err := needFile(r); err != nil {
			return err
		}
		if format != "json" && format != "csv" {
			return httpx.Validation("format must be json or csv")
		}
	}
	t, err := s.target(r)
	if err != nil {
		return err
	}
	g, err := s.BuildGSTR1(r.Context(), t)
	if err != nil {
		return err
	}
	if format != "" {
		return s.writeGSTR1(w, g, format, httpx.Query(r, "section"))
	}
	return httpx.OK(w, g)
}

func (s *service) gstr1Export(w http.ResponseWriter, r *http.Request) error {
	format := httpx.Query(r, "format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "csv" {
		return httpx.Validation("format must be json or csv")
	}
	t, err := s.target(r)
	if err != nil {
		return err
	}
	g, err := s.BuildGSTR1(r.Context(), t)
	if err != nil {
		return err
	}
	return s.writeGSTR1(w, g, format, httpx.Query(r, "section"))
}

// BuildGSTR3B streams every document of the period and adds opening ITC and the 2B view.
func (s *service) BuildGSTR3B(ctx context.Context, t target) (GSTR3B, error) {
	p := auth.FromContext(ctx)
	b := NewGSTR3B(t.reg.GSTIN, t.period, t.reg.StateCode)
	err := s.d.Invoices.EachInvoice(ctx, InvoiceQuery{GSTIN: t.reg.GSTIN, From: t.from, To: t.to,
		DocTypes: []string{"sale", "sale_return", "purchase", "purchase_return"}, IncludeLines: true},
		func(inv Invoice) error { b.Add(inv); return nil })
	if err != nil {
		return GSTR3B{}, err
	}
	var opening Taxes
	var twoB *recon.ITCSummary
	if s.d.DB != nil {
		if opening, _, err = s.opening(ctx, p.TenantID, t.reg.GSTIN, t.period); err != nil {
			return GSTR3B{}, err
		}
		sum, ok, err := s.rc.ITCFor2B(ctx, p.TenantID, t.reg.GSTIN, t.period)
		if err != nil {
			return GSTR3B{}, err
		}
		if ok {
			twoB = &sum
		}
	}
	g := b.Build(opening, twoB)
	if t.reg.Composition {
		g.Warnings = append([]string{"this registration is under the composition scheme: it files CMP-08 and GSTR-4, not GSTR-1/3B"}, g.Warnings...)
	}
	return g, nil
}

func (s *service) gstr3b(w http.ResponseWriter, r *http.Request) error {
	format := httpx.Query(r, "format")
	if format != "" {
		if err := needFile(r); err != nil {
			return err
		}
		if format != "json" && format != "csv" {
			return httpx.Validation("format must be json or csv")
		}
	}
	t, err := s.target(r)
	if err != nil {
		return err
	}
	g, err := s.BuildGSTR3B(r.Context(), t)
	if err != nil {
		return err
	}
	base := "GSTR3B_" + g.Gstin + "_" + g.RetPeriod
	switch format {
	case "json":
		download(w, base+".json", "application/json; charset=utf-8")
		return httpx.OK(w, g.Portal())
	case "csv":
		download(w, base+".csv", "text/csv; charset=utf-8")
		return WriteGSTR3BCSV(w, g)
	}
	return httpx.OK(w, g)
}

// ---------- ITC opening balances ----------

// Opening is the electronic credit ledger balance brought into a period.
type Opening struct {
	GSTIN  string `json:"gstin"`
	Period string `json:"period"`
	IGST   Rs     `json:"igst"`
	CGST   Rs     `json:"cgst"`
	SGST   Rs     `json:"sgst"`
	Cess   Rs     `json:"cess"`
}

// Validate checks the input.
func (o *Opening) Validate() error {
	if _, _, err := Period(o.Period); err != nil {
		return err
	}
	if o.IGST < 0 || o.CGST < 0 || o.SGST < 0 || o.Cess < 0 {
		return httpx.Validation("opening ITC cannot be negative")
	}
	return nil
}

func (s *service) opening(ctx context.Context, tenantID, gstin, period string) (Taxes, bool, error) {
	var t Taxes
	var ig, cg, sg, cs int64
	err := s.d.DB.QueryRow(ctx, `SELECT igst, cgst, sgst, cess FROM itc_opening WHERE tenant_id=$1 AND gstin=$2 AND period=$3`,
		tenantID, gstin, period).Scan(&ig, &cg, &sg, &cs)
	if db.IsNotFound(err) {
		return t, false, nil
	}
	if err != nil {
		return t, false, err
	}
	return Taxes{IGST: Rs(ig), CGST: Rs(cg), SGST: Rs(sg), Cess: Rs(cs)}, true, nil
}

func (s *service) getOpening(w http.ResponseWriter, r *http.Request) error {
	t, err := s.target(r)
	if err != nil {
		return err
	}
	o, set, err := s.opening(r.Context(), auth.FromContext(r.Context()).TenantID, t.reg.GSTIN, t.period)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"gstin": t.reg.GSTIN, "period": t.period, "set": set,
		"igst": o.IGST, "cgst": o.CGST, "sgst": o.SGST, "cess": o.Cess})
}

func (s *service) putOpening(w http.ResponseWriter, r *http.Request) error {
	var in Opening
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	want := in.GSTIN
	if want == "" {
		want = httpx.Query(r, "gstin")
	}
	reg, err := s.registration(r.Context(), want)
	if err != nil {
		return err
	}
	p := auth.FromContext(r.Context())
	in.GSTIN = reg.GSTIN
	if _, err := s.d.DB.Exec(r.Context(), `INSERT INTO itc_opening (tenant_id, gstin, period, igst, cgst, sgst, cess, updated_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (tenant_id, gstin, period) DO UPDATE SET igst=EXCLUDED.igst, cgst=EXCLUDED.cgst, sgst=EXCLUDED.sgst,
			cess=EXCLUDED.cess, updated_by=EXCLUDED.updated_by, updated_at=now()`,
		p.TenantID, in.GSTIN, in.Period, int64(in.IGST), int64(in.CGST), int64(in.SGST), int64(in.Cess), p.UserID()); err != nil {
		return err
	}
	return httpx.OK(w, in)
}
