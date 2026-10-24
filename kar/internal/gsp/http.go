package gsp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/taraju/mool/gst"
)

// HTTPPaths are the GSP endpoints, relative to BaseURL. %s placeholders are
// filled (URL-escaped) in the order documented on each field. The defaults
// mirror the NIC API paths most GSPs proxy, but every GSP differs: verify each
// one against your GSP's sandbox before going live.
type HTTPPaths struct {
	Search      string // GET, %s = GSTIN
	GenerateIRN string // POST INV-01 JSON
	CancelIRN   string // POST {Irn, CnlRsn, CnlRem}
	IRNByDoc    string // GET, %s = doc type, doc no, doc date (dd/mm/yyyy)
	EWBByIRN    string // POST {Irn, Distance, …}
	GenerateEWB string // POST NIC e-way JSON
	PartB       string // POST vehicle update
	Extend      string // POST extend validity
	CancelEWB   string // POST cancel
	GetEWB      string // GET, %s = EWB number
	GSTR2B      string // GET, %s = GSTIN, period (MMYYYY)
}

// DefaultHTTPPaths are placeholders modelled on the NIC API — verify against your GSP.
var DefaultHTTPPaths = HTTPPaths{
	Search:      "/commonapi/v1.1/search?action=TP&gstin=%s",
	GenerateIRN: "/eicore/v1.03/Invoice",
	CancelIRN:   "/eicore/v1.03/Invoice/Cancel",
	IRNByDoc:    "/eicore/v1.03/Invoice/irnbydocdetails?doctype=%s&docnum=%s&docdate=%s",
	EWBByIRN:    "/eiewb/v1.03/ewaybill",
	GenerateEWB: "/ewaybillapi/v1.03/ewayapi?action=GENEWAYBILL",
	PartB:       "/ewaybillapi/v1.03/ewayapi?action=VEHEWB",
	Extend:      "/ewaybillapi/v1.03/ewayapi?action=EXTENDVALIDITY",
	CancelEWB:   "/ewaybillapi/v1.03/ewayapi?action=CANEWB",
	GetEWB:      "/ewaybillapi/v1.03/ewayapi/GetEwayBill?ewbNo=%s",
	GSTR2B:      "/gstr2b/v1/get?gstin=%s&rtnprd=%s",
}

// HTTPConfig configures the generic GSP driver.
type HTTPConfig struct {
	BaseURL      string
	ClientID     string // OAuth2 client-credentials; empty = no token exchange
	ClientSecret string
	TokenPath    string // default /oauth/token
	Paths        HTTPPaths
	// Per-GSTIN API user headers (names vary by GSP).
	HeaderGSTIN    string // default "gstin"
	HeaderUsername string // default "user_name"
	HeaderPassword string // default "password"
	Timeout        time.Duration
	Log            *slog.Logger
}

// HTTP is a configurable GSP driver.
type HTTP struct {
	cfg  HTTPConfig
	http *http.Client
	log  *slog.Logger

	mu      sync.Mutex
	token   string
	expires time.Time
	now     func() time.Time
}

// NewHTTP builds the driver, filling defaults for empty paths and headers.
func NewHTTP(cfg HTTPConfig) (*HTTP, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("GSP_BASE_URL is required for the http GSP driver")
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.TokenPath == "" {
		cfg.TokenPath = "/oauth/token"
	}
	d := DefaultHTTPPaths
	fill := func(dst *string, def string) {
		if *dst == "" {
			*dst = def
		}
	}
	p := &cfg.Paths
	fill(&p.Search, d.Search)
	fill(&p.GenerateIRN, d.GenerateIRN)
	fill(&p.CancelIRN, d.CancelIRN)
	fill(&p.IRNByDoc, d.IRNByDoc)
	fill(&p.EWBByIRN, d.EWBByIRN)
	fill(&p.GenerateEWB, d.GenerateEWB)
	fill(&p.PartB, d.PartB)
	fill(&p.Extend, d.Extend)
	fill(&p.CancelEWB, d.CancelEWB)
	fill(&p.GetEWB, d.GetEWB)
	fill(&p.GSTR2B, d.GSTR2B)
	fill(&cfg.HeaderGSTIN, "gstin")
	fill(&cfg.HeaderUsername, "user_name")
	fill(&cfg.HeaderPassword, "password")
	if cfg.Timeout <= 0 {
		cfg.Timeout = 15 * time.Second
	}
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	return &HTTP{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}, log: log, now: time.Now}, nil
}

func (h *HTTP) Name() string { return "http" }

// ---------- OAuth2 client credentials ----------

func (h *HTTP) accessToken(ctx context.Context) (string, error) {
	if h.cfg.ClientID == "" {
		return "", nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.token != "" && h.now().Before(h.expires) {
		return h.token, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {h.cfg.ClientID}, "client_secret": {h.cfg.ClientSecret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.cfg.BaseURL+h.cfg.TokenPath, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	start := h.now()
	res, err := h.http.Do(req)
	if err != nil {
		h.log.Warn("gsp token request failed", "error", err.Error())
		return "", errf(KindUnavailable, CodeProviderDown, "", "gsp token request: %s", err.Error())
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	h.log.Info("gsp token", "status", res.StatusCode, "duration_ms", h.now().Sub(start).Milliseconds())
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusBadRequest {
		return "", errf(KindAuth, CodeAuthFailed, "", "gsp rejected the client credentials (HTTP %d)", res.StatusCode)
	}
	if res.StatusCode >= 300 {
		return "", errf(KindUnavailable, CodeProviderDown, "", "gsp token endpoint returned %d", res.StatusCode)
	}
	var tok struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil || tok.AccessToken == "" {
		return "", errf(KindAuth, CodeAuthFailed, "", "gsp token response has no access_token")
	}
	secs, _ := strconv.Atoi(strings.Trim(string(tok.ExpiresIn), `"`))
	if secs <= 0 {
		secs = 3600
	}
	margin := 60
	if secs <= 2*margin {
		margin = secs / 4
	}
	h.token, h.expires = tok.AccessToken, h.now().Add(time.Duration(secs-margin)*time.Second)
	return h.token, nil
}

func (h *HTTP) dropToken() {
	h.mu.Lock()
	h.token = ""
	h.mu.Unlock()
}

// ---------- transport ----------

type userKind int

const (
	userNone userKind = iota
	userEInvoice
	userEWay
)

func escapeAll(a []string) []any {
	out := make([]any, len(a))
	for i, s := range a {
		out[i] = url.QueryEscape(s)
	}
	return out
}

// call sends one request and returns the unwrapped data of the GSP envelope.
func (h *HTTP) call(ctx context.Context, op, method, pathTmpl string, args []string, c Credentials, user userKind, body any) (json.RawMessage, error) {
	path := pathTmpl
	if len(args) > 0 {
		path = fmt.Sprintf(pathTmpl, escapeAll(args)...)
	}
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return nil, errf(KindValidation, CodeInvalidField, "", "payload: %s", err.Error())
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := h.accessToken(ctx)
		if err != nil {
			return nil, err
		}
		var rdr io.Reader
		if payload != nil {
			rdr = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, h.cfg.BaseURL+path, rdr)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		if c.GSTIN != "" {
			req.Header.Set(h.cfg.HeaderGSTIN, c.GSTIN)
		}
		switch user {
		case userEInvoice:
			if c.EInvoiceUsername == "" || c.EInvoicePassword == "" {
				return nil, errf(KindAuth, CodeAuthFailed, "", "no e-invoice API user saved for GSTIN %s", c.GSTIN)
			}
			req.Header.Set(h.cfg.HeaderUsername, c.EInvoiceUsername)
			req.Header.Set(h.cfg.HeaderPassword, c.EInvoicePassword)
		case userEWay:
			if c.EWayUsername == "" || c.EWayPassword == "" {
				return nil, errf(KindAuth, CodeAuthFailed, "", "no e-way bill API user saved for GSTIN %s", c.GSTIN)
			}
			req.Header.Set(h.cfg.HeaderUsername, c.EWayUsername)
			req.Header.Set(h.cfg.HeaderPassword, c.EWayPassword)
		}
		start := h.now()
		res, err := h.http.Do(req)
		if err != nil {
			h.log.Warn("gsp call failed", "op", op, "gstin", c.GSTIN, "error", err.Error())
			return nil, errf(KindUnavailable, CodeProviderDown, "", "gsp %s: %s", op, err.Error())
		}
		raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
		res.Body.Close()
		if res.StatusCode == http.StatusUnauthorized && attempt == 0 && tok != "" {
			h.dropToken() // token revoked or expired early: fetch a new one once
			continue
		}
		data, gerr := parseEnvelope(res.StatusCode, raw)
		attrs := []any{"op", op, "method", method, "path", redactPath(path), "gstin", c.GSTIN, "status", res.StatusCode,
			"duration_ms", h.now().Sub(start).Milliseconds(), "request_bytes", len(payload), "response_bytes", len(raw)}
		if gerr != nil {
			attrs = append(attrs, "error_kind", string(gerr.Kind), "error_code", gerr.Code)
			h.log.Warn("gsp call", attrs...)
		} else {
			h.log.Info("gsp call", attrs...)
		}
		if h.log.Enabled(ctx, slog.LevelDebug) {
			h.log.Debug("gsp call bodies", "op", op, "request", Redact(payload), "response", Redact(raw))
		}
		if gerr != nil {
			return nil, gerr
		}
		return data, nil
	}
	return nil, errf(KindAuth, CodeAuthFailed, "", "gsp %s: unauthorized", op)
}

var secretKey = regexp.MustCompile(`(?i)(password|passwd|secret|token|sek|appkey|app_key|authorization|otp)`)

// Redact returns body with secret-looking JSON fields masked, truncated to 4 KB.
func Redact(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return "[non-JSON body omitted]"
	}
	b, _ := json.Marshal(redactValue(v))
	if len(b) > 4096 {
		return string(b[:4096]) + "…"
	}
	return string(b)
}

func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if secretKey.MatchString(k) {
				t[k] = "***"
			} else {
				t[k] = redactValue(x)
			}
		}
	case []any:
		for i := range t {
			t[i] = redactValue(t[i])
		}
	}
	return v
}

var pathSecret = regexp.MustCompile(`(?i)((?:password|secret|token)=)[^&]*`)

func redactPath(p string) string { return pathSecret.ReplaceAllString(p, "${1}***") }

// envelope is the NIC response shape most GSPs pass through.
type envelope struct {
	Status       json.RawMessage `json:"Status"`
	StatusLower  json.RawMessage `json:"status"`
	Data         json.RawMessage `json:"Data"`
	DataLower    json.RawMessage `json:"data"`
	Result       json.RawMessage `json:"result"`
	Results      json.RawMessage `json:"results"`
	ErrorDetails []struct {
		ErrorCode    string `json:"ErrorCode"`
		ErrorMessage string `json:"ErrorMessage"`
	} `json:"ErrorDetails"`
	InfoDtls []struct {
		InfCd string          `json:"InfCd"`
		Desc  json.RawMessage `json:"Desc"`
	} `json:"InfoDtls"`
	Error   json.RawMessage `json:"error"`
	Message json.RawMessage `json:"message"`
}

func classify(code string) Kind {
	switch code {
	case CodeDuplicateIRN, CodeDuplicateEWB:
		return KindDuplicate
	case CodeCancelWindow, CodeIRNNotActive, CodeIRNHasEWB, CodeEWBCancelWindow, CodeEWBNotActive, CodeEWBExtendWindow:
		return KindNotAllowed
	case CodeIRNNotFound, CodeEWBNotFound:
		return KindNotFound
	}
	return KindValidation
}

func rawString(r json.RawMessage) string {
	if len(r) == 0 || string(r) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(r, &s) == nil {
		return s
	}
	return strings.Trim(string(r), `"`)
}

// parseEnvelope maps an HTTP answer to data or a typed error.
func parseEnvelope(status int, raw []byte) (json.RawMessage, *Error) {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return nil, errf(KindAuth, CodeAuthFailed, "", "gsp rejected the credentials (HTTP %d)", status)
	}
	if status >= 500 || status == http.StatusTooManyRequests {
		return nil, errf(KindUnavailable, CodeProviderDown, "", "gsp returned HTTP %d", status)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		if status >= 300 {
			return nil, errf(KindUnavailable, CodeProviderDown, "", "gsp returned HTTP %d", status)
		}
		return nil, errf(KindUnavailable, CodeProviderDown, "", "gsp response is not JSON")
	}
	if len(env.ErrorDetails) > 0 {
		e := &Error{Code: env.ErrorDetails[0].ErrorCode, Message: env.ErrorDetails[0].ErrorMessage}
		for _, d := range env.ErrorDetails {
			e.Details = append(e.Details, Detail{Code: d.ErrorCode, Message: d.ErrorMessage})
		}
		e.Kind = classify(e.Code)
		if e.Kind == KindDuplicate {
			for _, inf := range env.InfoDtls {
				if inf.InfCd == "DUPIRN" && len(inf.Desc) > 0 {
					if r, err := parseIRN(inf.Desc); err == nil {
						e.Existing = &r
					}
				}
			}
		}
		return nil, e
	}
	st := rawString(env.Status)
	if st == "" {
		st = rawString(env.StatusLower)
	}
	if st == "0" || strings.EqualFold(st, "failed") || strings.EqualFold(st, "error") || status >= 300 {
		msg := rawString(env.Error)
		if msg == "" {
			msg = rawString(env.Message)
		}
		if msg == "" {
			msg = fmt.Sprintf("gsp returned HTTP %d", status)
		}
		code := ""
		if m := regexp.MustCompile(`\b([0-9]{3,4})\b`).FindStringSubmatch(msg); m != nil {
			code = m[1]
		}
		return nil, &Error{Kind: classify(code), Code: code, Message: msg}
	}
	for _, cand := range []json.RawMessage{env.Data, env.DataLower, env.Result} {
		if s := strings.TrimSpace(string(cand)); s != "" && s != "null" {
			// NIC sometimes returns Data as a JSON string.
			if s[0] == '"' {
				var inner string
				if json.Unmarshal(cand, &inner) == nil && strings.HasPrefix(strings.TrimSpace(inner), "{") {
					return json.RawMessage(inner), nil
				}
			}
			return cand, nil
		}
	}
	if len(env.Results) > 0 {
		var r struct {
			Message json.RawMessage `json:"message"`
			Status  string          `json:"status"`
		}
		if json.Unmarshal(env.Results, &r) == nil && len(r.Message) > 0 {
			if strings.EqualFold(r.Status, "failed") {
				msg := rawString(r.Message)
				return nil, &Error{Kind: KindValidation, Message: msg}
			}
			return r.Message, nil
		}
	}
	return raw, nil
}

// ---------- field extraction ----------

func fields(raw json.RawMessage) map[string]any {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var m map[string]any
	_ = d.Decode(&m)
	return m
}

func str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch t := v.(type) {
			case string:
				if t != "" {
					return t
				}
			case json.Number:
				return t.String()
			default:
				return fmt.Sprint(t)
			}
		}
	}
	return ""
}

var timeLayouts = []string{NICTimeLayout, EWBTimeLayout, "02/01/2006 15:04:05", "02/01/2006 03:04:05PM", time.RFC3339, "2006-01-02T15:04:05", "02/01/2006"}

// ParseNICTime parses the timestamp formats the IRP and NIC use (IST).
func ParseNICTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, l := range timeLayouts {
		if t, err := time.ParseInLocation(l, s, IST); err == nil {
			return t
		}
	}
	return time.Time{}
}

func parseIRN(raw json.RawMessage) (IRNResult, error) {
	m := fields(raw)
	if m == nil {
		return IRNResult{}, errors.New("irn response is not an object")
	}
	r := IRNResult{
		IRN: str(m, "Irn", "irn"), AckNo: str(m, "AckNo", "ackNo", "ack_no"), AckDt: ParseNICTime(str(m, "AckDt", "ackDt", "ack_dt")),
		SignedInvoice: str(m, "SignedInvoice", "signedInvoice"), SignedQR: str(m, "SignedQRCode", "SignedQR", "signedQRCode"),
		Status: str(m, "Status", "status"), EWBNo: str(m, "EwbNo", "ewbNo"), EWBDate: ParseNICTime(str(m, "EwbDt", "ewbDt")),
		EWBValidUpto: ParseNICTime(str(m, "EwbValidTill", "ewbValidTill")), CancelDate: ParseNICTime(str(m, "CancelDate", "cancelDate")),
	}
	if r.Status == "" {
		r.Status = IRNActive
	}
	if r.IRN == "" {
		return r, errors.New("irn response has no Irn")
	}
	return r, nil
}

func parseEWB(raw json.RawMessage) (EWBResult, error) {
	m := fields(raw)
	r := EWBResult{
		EWBNo:     str(m, "ewayBillNo", "EwbNo", "ewbNo", "ewb_no"),
		EWBDate:   ParseNICTime(str(m, "ewayBillDate", "EwbDt", "ewbDate", "vehUpdDate")),
		ValidUpto: ParseNICTime(str(m, "validUpto", "EwbValidTill", "validUpTo")),
		Alert:     str(m, "alert", "Remarks"),
	}
	if r.EWBNo == "" {
		return r, errors.New("e-way response has no bill number")
	}
	return r, nil
}

// ---------- Client ----------

func (h *HTTP) SearchGSTIN(ctx context.Context, g string) (Taxpayer, error) {
	g = gst.Normalize(g)
	data, err := h.call(ctx, "search_gstin", http.MethodGet, h.cfg.Paths.Search, []string{g}, Credentials{}, userNone, nil)
	if err != nil {
		return Taxpayer{}, err
	}
	return ParseTaxpayer(g, data)
}

func (h *HTTP) GenerateIRN(ctx context.Context, c Credentials, payload any) (IRNResult, error) {
	data, err := h.call(ctx, "generate_irn", http.MethodPost, h.cfg.Paths.GenerateIRN, nil, c, userEInvoice, payload)
	if err != nil {
		return IRNResult{}, err
	}
	r, perr := parseIRN(data)
	if perr != nil {
		return IRNResult{}, errf(KindUnavailable, CodeProviderDown, "", "gsp generate IRN: %s", perr.Error())
	}
	return r, nil
}

func (h *HTTP) CancelIRN(ctx context.Context, c Credentials, req CancelIRNRequest) (IRNResult, error) {
	body := map[string]any{"Irn": req.IRN, "CnlRsn": strconv.Itoa(req.ReasonCode), "CnlRem": req.Remark}
	data, err := h.call(ctx, "cancel_irn", http.MethodPost, h.cfg.Paths.CancelIRN, nil, c, userEInvoice, body)
	if err != nil {
		return IRNResult{}, err
	}
	m := fields(data)
	r := IRNResult{IRN: str(m, "Irn", "irn"), Status: IRNCancelled, CancelDate: ParseNICTime(str(m, "CancelDate", "cancelDate"))}
	if r.IRN == "" {
		r.IRN = req.IRN
	}
	return r, nil
}

func (h *HTTP) GetIRNByDocument(ctx context.Context, c Credentials, ref DocumentRef) (IRNResult, error) {
	data, err := h.call(ctx, "irn_by_doc", http.MethodGet, h.cfg.Paths.IRNByDoc, []string{ref.DocType, ref.DocNo, ref.DocDate}, c, userEInvoice, nil)
	if err != nil {
		return IRNResult{}, err
	}
	r, perr := parseIRN(data)
	if perr != nil {
		return IRNResult{}, errf(KindNotFound, CodeIRNNotFound, "", "gsp: %s", perr.Error())
	}
	return r, nil
}

func (h *HTTP) GenerateEWB(ctx context.Context, c Credentials, req EWBRequest) (EWBResult, error) {
	var data json.RawMessage
	var err error
	switch {
	case req.ByIRN != nil:
		data, err = h.call(ctx, "ewb_by_irn", http.MethodPost, h.cfg.Paths.EWBByIRN, nil, c, userEInvoice, req.ByIRN)
	case req.Bill != nil:
		data, err = h.call(ctx, "generate_ewb", http.MethodPost, h.cfg.Paths.GenerateEWB, nil, c, userEWay, req.Bill)
	default:
		return EWBResult{}, errf(KindValidation, CodeEWBInvalid, "", "give an IRN or a standalone e-way payload")
	}
	if err != nil {
		return EWBResult{}, err
	}
	r, perr := parseEWB(data)
	if perr != nil {
		return EWBResult{}, errf(KindUnavailable, CodeProviderDown, "", "gsp generate e-way bill: %s", perr.Error())
	}
	return r, nil
}

func (h *HTTP) UpdatePartB(ctx context.Context, c Credentials, req PartBRequest) (EWBResult, error) {
	data, err := h.call(ctx, "ewb_part_b", http.MethodPost, h.cfg.Paths.PartB, nil, c, userEWay, req)
	if err != nil {
		return EWBResult{}, err
	}
	r, _ := parseEWB(data)
	if r.EWBNo == "" {
		r.EWBNo = req.EWBNo
	}
	return r, nil
}

func (h *HTTP) ExtendValidity(ctx context.Context, c Credentials, req ExtendRequest) (EWBResult, error) {
	data, err := h.call(ctx, "ewb_extend", http.MethodPost, h.cfg.Paths.Extend, nil, c, userEWay, req)
	if err != nil {
		return EWBResult{}, err
	}
	r, _ := parseEWB(data)
	if r.EWBNo == "" {
		r.EWBNo = req.EWBNo
	}
	return r, nil
}

func (h *HTTP) CancelEWB(ctx context.Context, c Credentials, req CancelEWBRequest) (time.Time, error) {
	data, err := h.call(ctx, "ewb_cancel", http.MethodPost, h.cfg.Paths.CancelEWB, nil, c, userEWay, req)
	if err != nil {
		return time.Time{}, err
	}
	t := ParseNICTime(str(fields(data), "cancelDate", "CancelDate"))
	if t.IsZero() {
		t = h.now()
	}
	return t, nil
}

func (h *HTTP) GetEWB(ctx context.Context, c Credentials, no string) (EWBDetails, error) {
	data, err := h.call(ctx, "ewb_get", http.MethodGet, h.cfg.Paths.GetEWB, []string{no}, c, userEWay, nil)
	if err != nil {
		return EWBDetails{}, err
	}
	m := fields(data)
	return EWBDetails{EWBNo: str(m, "ewbNo", "ewayBillNo"), Status: str(m, "status"), EWBDate: ParseNICTime(str(m, "ewayBillDate")),
		ValidUpto: ParseNICTime(str(m, "validUpto")), GSTIN: str(m, "userGstin", "fromGstin"), DocNo: str(m, "docNo"), Raw: data}, nil
}

func (h *HTTP) FetchGSTR2B(ctx context.Context, c Credentials, g, period string) (json.RawMessage, error) {
	return h.call(ctx, "gstr2b", http.MethodGet, h.cfg.Paths.GSTR2B, []string{gst.Normalize(g), period}, c, userNone, nil)
}

var _ Client = (*HTTP)(nil)
