// Package gstin verifies GSTINs: checksum first, then a live lookup through a
// GST Suvidha Provider (GSP), with a 7-day cache.
package gstin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/taraju/mool/gst"
)

// Result is the verification answer (shape fixed in ARCHITECTURE.md §6).
type Result struct {
	GSTIN            string `json:"gstin"`
	Valid            bool   `json:"valid"`
	LegalName        string `json:"legal_name"`
	TradeName        string `json:"trade_name"`
	Status           string `json:"status"` // Active | Cancelled | Suspended | Inactive ("" when unknown)
	StateCode        string `json:"state_code"`
	Address          string `json:"address"`
	RegistrationType string `json:"registration_type"`
	Source           string `json:"source"` // gsp | cache | checksum_only
	Message          string `json:"message,omitempty"`
}

// Provider performs a live taxpayer lookup.
type Provider interface {
	Name() string
	Lookup(ctx context.Context, gstin string) (Result, error)
}

// ChecksumOnly needs no contract: it validates the checksum and derives the
// state from the first two digits. Names and status stay empty.
type ChecksumOnly struct{}

func (ChecksumOnly) Name() string { return "checksum_only" }

func (ChecksumOnly) Lookup(_ context.Context, g string) (Result, error) {
	return checksumResult(g), nil
}

func checksumResult(g string) Result {
	g = gst.Normalize(g)
	r := Result{GSTIN: g, Source: "checksum_only"}
	if err := gst.ValidateGSTIN(g); err != nil {
		r.Message = err.Error()
		return r
	}
	r.Valid = true
	r.StateCode = gst.StateCode(g)
	return r
}

// GSP calls a GST Suvidha Provider's "search taxpayer" API. Every GSP (Masters
// India, ClearTax, Cygnet, …) wraps the GSTN public search response, so the
// URL and auth headers are configurable and the parser accepts the payload
// either at the top level or under "data"/"result".
//
// A signed GSP contract is required for production credentials.
type GSP struct {
	BaseURL      string // GSP_BASE_URL
	SearchPath   string // GSP_SEARCH_PATH, %s is replaced by the GSTIN
	ClientID     string // GSP_CLIENT_ID     → header client_id
	ClientSecret string // GSP_CLIENT_SECRET → header client_secret
	HTTP         *http.Client
}

// NewGSP builds the GSP driver.
func NewGSP(baseURL, searchPath, clientID, clientSecret string, timeout time.Duration) *GSP {
	if searchPath == "" {
		searchPath = "/commonapi/v1.1/search?action=TP&gstin=%s"
	}
	return &GSP{
		BaseURL: strings.TrimRight(baseURL, "/"), SearchPath: searchPath,
		ClientID: clientID, ClientSecret: clientSecret, HTTP: &http.Client{Timeout: timeout},
	}
}

func (g *GSP) Name() string { return "gsp" }

func (g *GSP) Lookup(ctx context.Context, gstin string) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.BaseURL+fmt.Sprintf(g.SearchPath, gstin), nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("client_id", g.ClientID)
	req.Header.Set("client_secret", g.ClientSecret)
	res, err := g.HTTP.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("gsp request: %w", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		return Result{}, fmt.Errorf("gsp returned %d", res.StatusCode)
	}
	return ParseGSPResponse(gstin, body)
}

type taxpayer struct {
	GSTIN    string `json:"gstin"`
	Lgnm     string `json:"lgnm"`
	TradeNam string `json:"tradeNam"`
	Sts      string `json:"sts"`
	Dty      string `json:"dty"`
	Pradr    struct {
		Addr struct {
			Bnm, Bno, Flno, St, Loc, City, Dst, Stcd, Pncd string
		} `json:"addr"`
		Adr string `json:"adr"`
	} `json:"pradr"`
}

// ParseGSPResponse maps a GSTN-style taxpayer search payload to a Result.
func ParseGSPResponse(gstin string, body []byte) (Result, error) {
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return Result{}, fmt.Errorf("gsp response: %w", err)
	}
	raw := json.RawMessage(body)
	for _, k := range []string{"data", "result", "taxpayerInfo"} {
		if v, ok := wrapper[k]; ok && len(v) > 0 && v[0] == '{' {
			raw = v
			break
		}
	}
	var tp taxpayer
	if err := json.Unmarshal(raw, &tp); err != nil {
		return Result{}, fmt.Errorf("gsp response: %w", err)
	}
	if tp.Lgnm == "" && tp.TradeNam == "" {
		if msg := errorMessage(wrapper); msg != "" {
			return Result{}, fmt.Errorf("gsp: %s", msg)
		}
		return Result{}, fmt.Errorf("gsp: taxpayer not found")
	}
	g := gst.Normalize(gstin)
	a := tp.Pradr.Addr
	addr := tp.Pradr.Adr
	if addr == "" {
		addr = joinNonEmpty(", ", a.Bno, a.Flno, a.Bnm, a.St, a.Loc, a.City, a.Dst, a.Stcd, a.Pncd)
	}
	return Result{
		GSTIN: g, Valid: true, LegalName: tp.Lgnm, TradeName: tp.TradeNam,
		Status: normalizeStatus(tp.Sts), StateCode: gst.StateCode(g), Address: addr,
		RegistrationType: tp.Dty, Source: "gsp",
	}, nil
}

func errorMessage(w map[string]json.RawMessage) string {
	for _, k := range []string{"message", "error", "errorMessage"} {
		if v, ok := w[k]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil && s != "" {
				return s
			}
			return strings.Trim(string(v), `"`)
		}
	}
	return ""
}

func normalizeStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "active":
		return "Active"
	case "cancelled", "canceled":
		return "Cancelled"
	case "suspended":
		return "Suspended"
	case "inactive":
		return "Inactive"
	}
	return strings.TrimSpace(s)
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}
