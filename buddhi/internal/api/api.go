// Package api wires buddhi's HTTP routes.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/maapan/buddhi/internal/anomaly"
	"github.com/maapan/buddhi/internal/ask"
	"github.com/maapan/buddhi/internal/clients"
	"github.com/maapan/buddhi/internal/jobs"
	"github.com/maapan/buddhi/internal/kpi"
	"github.com/maapan/buddhi/internal/llm"
	"github.com/maapan/buddhi/internal/scan"
	"github.com/maapan/buddhi/internal/store"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/events"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/logx"
)

// MaxUploadBytes caps scanned bill files.
const MaxUploadBytes = 10 << 20

// API holds buddhi's dependencies.
type API struct {
	DB         *pgxpool.Pool
	LLM        llm.Client
	Guard      *store.Guard
	KPI        *kpi.Service
	Clients    *clients.Clients
	Forecaster *jobs.Forecaster
	Weekly     *jobs.Weekly
	Log        *slog.Logger
	Now        func() time.Time
}

// Routes registers everything on mux.
func (a *API) Routes(mux *http.ServeMux, v *auth.Verifier, trustIAM bool) {
	user := func(h httpx.HandlerFunc, perms ...string) http.Handler {
		return v.User(auth.Require(httpx.H(h), perms...))
	}
	mux.Handle("POST /v1/scan/purchase-bill", user(a.scanBill, auth.PermAIUse, auth.PermPurchaseManage))
	mux.Handle("POST /v1/scan/match-items", user(a.matchItems, auth.PermAIUse, auth.PermPurchaseManage))
	mux.Handle("GET /v1/insights/gst", user(a.gstInsights, auth.PermAIUse, auth.PermReportRead))
	mux.Handle("POST /v1/ask", user(a.ask, auth.PermAIUse))
	mux.Handle("GET /v1/forecast/reorder", user(a.reorder, auth.PermAIUse))
	mux.Handle("GET /v1/insights/anomalies", user(a.anomalies, auth.PermAIUse))
	mux.Handle("POST /v1/insights/anomalies/{id}/resolve", user(a.resolve, auth.PermAIUse))
	mux.Handle("GET /v1/dashboard", user(a.dashboard, auth.PermReportRead))
	mux.Handle("GET /v1/ai/usage", user(a.usage, auth.PermAIUse))
	mux.Handle("GET /v1/ai/settings", user(a.getSettings, auth.PermAIUse))
	mux.Handle("PUT /v1/ai/settings", user(a.putSettings, auth.PermSettingsManage))

	mux.Handle("POST /internal/v1/jobs/forecast", v.Internal(httpx.H(a.jobForecast)))
	mux.Handle("POST /internal/v1/jobs/weekly-summary", v.Internal(httpx.H(a.jobWeekly)))
	mux.Handle("POST /internal/v1/jobs/gst-insights", v.Internal(httpx.H(a.jobGSTInsights)))

	mux.Handle("POST /events", events.Handler(v, trustIAM, events.Router{
		events.InvoiceCreated:   a.KPI.OnInvoiceCreated,
		events.InvoiceCancelled: a.KPI.OnInvoiceCancelled,
		events.NoteCreated:      a.KPI.OnNoteCreated,
		events.PaymentCaptured:  a.KPI.OnPaymentCaptured,
		events.StockLow:         a.KPI.OnStockLow,
	}))
}

// aiError maps LLM failures to API errors.
func aiError(err error) error {
	var refusal *llm.RefusalError
	var up *llm.UpstreamError
	switch {
	case errors.Is(err, llm.ErrDisabled):
		return httpx.Unavailable("%s", err.Error())
	case errors.As(err, &refusal):
		return httpx.Validation("The AI declined this request. Please rephrase it.").WithDetails(map[string]any{"category": refusal.Category})
	case errors.As(err, &up) && up.Retryable:
		return httpx.Unavailable("AI service is busy, please retry in a minute")
	case errors.As(err, &up):
		return httpx.Upstream("AI service error (%d)", up.Status)
	}
	return err
}

// storeScope resolves the store a request is about and enforces store-scoped roles.
func storeScope(p *auth.Principal, requested string, allowAll bool) (string, error) {
	scoped := p.Claims != nil && auth.StoreScoped(p.Claims.Role)
	if requested == "" {
		if scoped || !allowAll {
			requested = p.StoreID
		}
	}
	if requested == "" {
		if !allowAll {
			return "", httpx.Validation("store_id is required")
		}
		return kpi.AllStores, nil
	}
	if requested == kpi.AllStores {
		if scoped {
			return "", httpx.Forbidden("your role sees only its own stores")
		}
		return requested, nil
	}
	if p.Claims != nil && !p.Claims.CanAccessStore(requested) {
		return "", httpx.Forbidden("no access to store")
	}
	return requested, nil
}

var allowedUploads = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/webp": true, "image/gif": true, "application/pdf": true,
}

func (a *API) scanBill(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	if err := a.Guard.CheckFlag(r.Context(), p.TenantID); err != nil {
		return err
	}
	if !a.LLM.Enabled() {
		return aiError(llm.ErrDisabled)
	}
	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadBytes+1<<20)
	if err := r.ParseMultipartForm(MaxUploadBytes); err != nil {
		return httpx.BadRequest("upload a bill photo or PDF (max 10 MB) as multipart field \"file\"")
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		return httpx.BadRequest("multipart field \"file\" is required")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxUploadBytes+1))
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > MaxUploadBytes {
		return httpx.Validation("file must be between 1 byte and 10 MB")
	}
	mt := http.DetectContentType(data)
	if i := strings.Index(mt, ";"); i > 0 {
		mt = mt[:i]
	}
	if !allowedUploads[mt] {
		return httpx.Validation("unsupported file type %s (use JPEG, PNG, WebP, GIF or PDF)", mt)
	}

	// Hold the call's tokens before spending them, so parallel scans cannot
	// overshoot the monthly budget.
	res, err := a.Guard.Reserve(r.Context(), p.TenantID, "scan", store.EstimateScan)
	if err != nil {
		return err
	}
	raw, usage, err := a.LLM.Extract(r.Context(), llm.ExtractRequest{
		System: scan.System, Prompt: scan.Prompt, Schema: scan.Schema, Effort: "medium",
		Attachment: llm.Attachment{MediaType: mt, Data: data},
	})
	a.settle(r, p, res, usage, err)
	if err != nil {
		return aiError(err)
	}
	var ext scan.Extracted
	if err := json.Unmarshal(raw, &ext); err != nil {
		return httpx.Upstream("AI returned an unexpected shape")
	}

	names := make([]string, 0, len(ext.Lines))
	for _, l := range ext.Lines {
		names = append(names, l.Name)
	}
	catalog, warnings := a.itemMaster(r, p.StoreID, names)
	storeState := ""
	if p.StoreID != "" {
		if st, err := a.Clients.Store(r.Context(), p.StoreID); err == nil {
			storeState = st.StateCode
		}
	}
	result := scan.Build(ext, catalog, storeState)
	result.Warnings = append(result.Warnings, warnings...)
	return httpx.OK(w, map[string]any{"result": result, "usage": usage})
}

type askInput struct {
	Question string `json:"question"`
}

func (in *askInput) Validate() error {
	n := len([]rune(strings.TrimSpace(in.Question)))
	if n < 2 || n > 2000 {
		return httpx.Validation("question must be 2 to 2000 characters")
	}
	return nil
}

func (a *API) ask(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	var in askInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	if err := a.Guard.CheckFlag(r.Context(), p.TenantID); err != nil {
		return err
	}
	if !a.LLM.Enabled() {
		return aiError(llm.ErrDisabled)
	}
	res, err := a.Guard.Reserve(r.Context(), p.TenantID, "ask", store.EstimateAsk)
	if err != nil {
		return err
	}
	var stores []string
	if p.Claims != nil && auth.StoreScoped(p.Claims.Role) {
		stores = p.Claims.Stores
	}
	x := &ask.Executor{Clients: a.Clients, KPI: a.KPI, Tenant: p.TenantID, Stores: stores}
	req := llm.ChatRequest{
		Tier: llm.Smart, Effort: "medium", System: ask.System, Prompt: ask.Prompt(in.Question, a.Now(), stores),
		Tools: ask.Tools, Exec: x.Run, MaxToolCalls: ask.MaxToolCalls, TokenBudget: res.Tokens,
	}

	if r.URL.Query().Get("stream") != "true" {
		answer, usage, err := a.LLM.Chat(r.Context(), req)
		a.settle(r, p, res, usage, err)
		if err != nil {
			return aiError(err)
		}
		return httpx.OK(w, map[string]any{"answer": answer, "usage": usage})
	}

	// Server-sent events: event "delta" per text chunk, then "done" or "error".
	fl, ok := w.(http.Flusher)
	if !ok {
		a.settle(r, p, res, llm.Usage{}, nil)
		return httpx.Internal("streaming unsupported")
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	send := func(event string, v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
		fl.Flush()
	}
	req.OnText = func(t string) { send("delta", map[string]string{"text": t}) }
	answer, usage, err := a.LLM.Chat(r.Context(), req)
	a.settle(r, p, res, usage, err)
	if err != nil {
		e := httpx.AsError(aiError(err))
		send("error", e)
		return nil
	}
	send("done", map[string]any{"answer": answer, "usage": usage})
	return nil
}

// settle logs real usage and frees the reservation. It runs on a context
// detached from the request so a client that hung up is still billed.
func (a *API) settle(r *http.Request, p *auth.Principal, res *store.Reservation, u llm.Usage, err error) {
	ctx := context.WithoutCancel(r.Context())
	if serr := a.Guard.Settle(ctx, res, p.UserID(), u, err == nil); serr != nil {
		logx.From(ctx).Warn("record usage", "error", serr.Error())
	}
}

func (a *API) reorder(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	if err := a.Guard.CheckFlag(r.Context(), p.TenantID); err != nil {
		return err
	}
	st, err := storeScope(p, httpx.Query(r, "store_id"), false)
	if err != nil {
		return err
	}
	lead, _ := strconv.Atoi(httpx.Query(r, "lead_days"))
	if lead < 0 || lead > 90 {
		return httpx.Validation("lead_days must be 0-90")
	}
	rep, err := a.Forecaster.Reorder(r.Context(), p.TenantID, st, lead)
	if err != nil {
		return err
	}
	return httpx.OK(w, rep)
}

func (a *API) anomalies(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	st, err := storeScope(p, httpx.Query(r, "store_id"), true)
	if err != nil {
		return err
	}
	if st == kpi.AllStores {
		st = ""
	}
	days := 30
	if d, err := strconv.Atoi(httpx.Query(r, "days")); err == nil && d > 0 {
		days = min(d, 180)
	}
	since := a.Now().AddDate(0, 0, -days)
	rep, err := anomaly.List(r.Context(), a.DB, p.TenantID, st, httpx.Query(r, "kind"), since, httpx.Query(r, "include_resolved") == "true")
	if err != nil {
		return err
	}
	return httpx.OK(w, rep)
}

func (a *API) resolve(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		return httpx.BadRequest("bad id")
	}
	ok, err := anomaly.Resolve(r.Context(), a.DB, p.TenantID, id)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.NotFound("anomaly not found")
	}
	return httpx.NoContent(w)
}

func (a *API) dashboard(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	st, err := storeScope(p, httpx.Query(r, "store_id"), true)
	if err != nil {
		return err
	}
	d, err := a.KPI.Dashboard(r.Context(), p.TenantID, st, httpx.Query(r, "date"))
	if err != nil {
		return err
	}
	if st == kpi.AllStores {
		d.Cards = append(d.Cards, a.gstCard(r, p)...)
	}
	return httpx.OK(w, d)
}

func (a *API) usage(w http.ResponseWriter, r *http.Request) error {
	p := auth.FromContext(r.Context())
	s, err := a.Guard.Settings(r.Context(), p.TenantID)
	if err != nil {
		return err
	}
	used, err := a.Guard.MonthUsage(r.Context(), p.TenantID)
	if err != nil {
		return err
	}
	rep, err := a.Guard.Report(r.Context(), p.TenantID)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{
		"month_start": a.Guard.MonthStart().Format(time.DateOnly), "tokens_used": used,
		"token_budget": a.Guard.Budget(s), "ai_enabled": s.AIEnabled, "ai_configured": a.LLM.Enabled(), "by_feature": rep,
	})
}

func (a *API) getSettings(w http.ResponseWriter, r *http.Request) error {
	s, err := a.Guard.Settings(r.Context(), auth.FromContext(r.Context()).TenantID)
	if err != nil {
		return err
	}
	return httpx.OK(w, s)
}

type settingsInput struct {
	AIEnabled          bool    `json:"ai_enabled"`
	MonthlyTokenBudget *int64  `json:"monthly_token_budget"`
	OwnerPhone         *string `json:"owner_phone"`
}

func (in *settingsInput) Validate() error {
	if in.MonthlyTokenBudget != nil && *in.MonthlyTokenBudget < 0 {
		return httpx.Validation("monthly_token_budget cannot be negative")
	}
	if in.OwnerPhone != nil && !strings.HasPrefix(*in.OwnerPhone, "+") {
		return httpx.Validation("owner_phone must be in +91XXXXXXXXXX format")
	}
	return nil
}

func (a *API) putSettings(w http.ResponseWriter, r *http.Request) error {
	var in settingsInput
	if err := httpx.Decode(r, &in); err != nil {
		return err
	}
	s, err := a.Guard.SaveSettings(r.Context(), store.Settings{
		TenantID: auth.FromContext(r.Context()).TenantID, AIEnabled: in.AIEnabled,
		MonthlyTokenBudget: in.MonthlyTokenBudget, OwnerPhone: in.OwnerPhone,
	})
	if err != nil {
		return err
	}
	return httpx.OK(w, s)
}

func (a *API) jobForecast(w http.ResponseWriter, r *http.Request) error {
	res, err := a.Forecaster.Run(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}

func (a *API) jobWeekly(w http.ResponseWriter, r *http.Request) error {
	res, err := a.Weekly.Run(r.Context())
	if err != nil {
		return aiError(err)
	}
	return httpx.OK(w, res)
}
