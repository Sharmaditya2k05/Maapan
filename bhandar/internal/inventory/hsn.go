package inventory

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/maapan/mool/gst"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/logx"
	"github.com/maapan/mool/svc"
)

// HSNSuggestion is one candidate HSN/SAC code for an item.
type HSNSuggestion struct {
	Code        string `json:"code"`
	Description string `json:"description,omitempty"`
	RateBP      *int   `json:"rate_bp,omitempty"` // GST rate in force (kar)
	CessBP      int    `json:"cess_bp,omitempty"`
	Source      string `json:"source"`         // items (the tenant's own master) | kar
	Uses        int    `json:"uses,omitempty"` // items already carrying the code
}

// HSNSuggestions is the response of GET /v1/hsn/suggest.
type HSNSuggestions struct {
	Suggestions []HSNSuggestion `json:"suggestions"`
	// Kar reports the HSN master: ok | unavailable (not configured or failed) |
	// search_unsupported (kar has no internal search yet; codes still checked).
	Kar string `json:"kar"`
}

// karRate is the subset of kar's hsn.Rate / hsn.Code bhandar reads.
type karRate struct {
	Code        string `json:"code"`
	Description string `json:"description"`
	RateBP      int    `json:"rate_bp"`
	CessBP      int    `json:"cess_bp"`
	Condition   string `json:"condition,omitempty"`
}

// SuggestHSN proposes HSN codes for an item name (and/or checks a code):
// codes the tenant already uses on items with similar names or the same
// category, then kar's HSN master (GET /internal/v1/hsn/{code} for a code,
// GET /internal/v1/hsn?q= for words when kar offers it).
func (s *Service) SuggestHSN(ctx context.Context, tenantID, name, code, category string, limit int) (*HSNSuggestions, error) {
	name, code, category = strings.TrimSpace(name), strings.TrimSpace(code), strings.TrimSpace(category)
	if name == "" && code == "" && category == "" {
		return nil, httpx.Validation("name, code or category is required")
	}
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	out := &HSNSuggestions{Suggestions: []HSNSuggestion{}, Kar: "unavailable"}
	byCode := map[string]int{}
	add := func(h HSNSuggestion) {
		if i, ok := byCode[h.Code]; ok {
			cur := &out.Suggestions[i]
			if cur.Description == "" {
				cur.Description = h.Description
			}
			if cur.RateBP == nil {
				cur.RateBP, cur.CessBP = h.RateBP, h.CessBP
			}
			cur.Uses += h.Uses
			return
		}
		byCode[h.Code] = len(out.Suggestions)
		out.Suggestions = append(out.Suggestions, h)
	}

	// 1. The tenant's own items: words of the name (each must match) or the category.
	words := significantWords(name)
	if len(words) > 0 || category != "" || code != "" {
		// Score = matching name words (+2 for the same category, +3 for a code
		// prefix); codes are ranked by their best item's score, then by use.
		args := []any{tenantID}
		var score []string
		for _, w := range words {
			args = append(args, "%"+w+"%")
			score = append(score, "(CASE WHEN name ILIKE $"+strconv.Itoa(len(args))+" THEN 1 ELSE 0 END)")
		}
		if category != "" {
			args = append(args, category)
			score = append(score, "(CASE WHEN category = $"+strconv.Itoa(len(args))+" THEN 2 ELSE 0 END)")
		}
		if code != "" {
			args = append(args, code+"%")
			score = append(score, "(CASE WHEN hsn LIKE $"+strconv.Itoa(len(args))+" THEN 3 ELSE 0 END)")
		}
		args = append(args, limit)
		rows, err := s.DB.Query(ctx, `SELECT hsn, count(*) FROM (SELECT hsn, `+strings.Join(score, " + ")+` AS score
			FROM items WHERE tenant_id = $1 AND hsn IS NOT NULL) x WHERE score > 0
			GROUP BY hsn ORDER BY max(score) DESC, count(*) DESC, hsn LIMIT $`+strconv.Itoa(len(args)), args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h HSNSuggestion
			if err := rows.Scan(&h.Code, &h.Uses); err != nil {
				rows.Close()
				return nil, err
			}
			h.Source = "items"
			add(h)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	// 2. kar's master.
	if s.Kar != nil && s.Kar.BaseURL != "" {
		kctx := svc.ForTenant(ctx, tenantID)
		out.Kar = "ok"
		on := s.today().Format(time.DateOnly)
		if code != "" && isDigits(code) {
			var c karRate
			err := s.Kar.Get(kctx, "/internal/v1/hsn/"+url.PathEscape(code)+"?on="+on, &c)
			switch {
			case err == nil:
				r := c.RateBP
				add(HSNSuggestion{Code: c.Code, Description: c.Description, RateBP: &r, CessBP: c.CessBP, Source: "kar"})
			case httpx.AsError(err).Status == 404:
				// not in the master: nothing to add
			default:
				logx.From(ctx).Warn("kar hsn lookup failed", "code", code, "error", err.Error())
				out.Kar = "unavailable"
			}
		}
		if q := strings.Join(words, " "); q != "" && out.Kar == "ok" {
			var res struct {
				Items []karRate `json:"items"`
			}
			err := s.Kar.Get(kctx, "/internal/v1/hsn?q="+url.QueryEscape(q)+"&on="+on+"&limit="+strconv.Itoa(limit), &res)
			switch {
			case err == nil:
				for _, c := range res.Items {
					r := c.RateBP
					add(HSNSuggestion{Code: c.Code, Description: c.Description, RateBP: &r, CessBP: c.CessBP, Source: "kar"})
				}
			case httpx.AsError(err).Status == 404 || httpx.AsError(err).Status == 405:
				out.Kar = "search_unsupported"
			default:
				logx.From(ctx).Warn("kar hsn search failed", "error", err.Error())
				out.Kar = "unavailable"
			}
		}
		// Rates for codes found only in the tenant's items (a handful of calls).
		if out.Kar == "ok" || out.Kar == "search_unsupported" {
			for i := range out.Suggestions {
				h := &out.Suggestions[i]
				if h.RateBP != nil || i >= 5 {
					continue
				}
				var c karRate
				if err := s.Kar.Get(kctx, "/internal/v1/hsn/"+url.PathEscape(h.Code)+"?on="+on, &c); err == nil {
					r := c.RateBP
					h.RateBP, h.CessBP, h.Description = &r, c.CessBP, c.Description
				}
			}
		}
	}
	// Codes already in use first, then kar's; stable within each.
	sort.SliceStable(out.Suggestions, func(i, j int) bool { return out.Suggestions[i].Uses > out.Suggestions[j].Uses })
	if len(out.Suggestions) > limit {
		out.Suggestions = out.Suggestions[:limit]
	}
	return out, nil
}

// autoHSN fills an item's empty HSN (and, for a taxable item sent with rate
// 0, a GST rate in force from kar) from the top suggestion. Failures only log.
func (s *Service) autoHSN(ctx context.Context, tenantID string, in *ItemInput) {
	code := in.HSN
	if code == "" {
		sg, err := s.SuggestHSN(ctx, tenantID, in.Name, "", in.Category, 1)
		if err != nil || len(sg.Suggestions) == 0 {
			return
		}
		h := sg.Suggestions[0]
		if !hsnPattern.MatchString(h.Code) {
			return
		}
		in.HSN = h.Code
		if h.RateBP != nil && in.TaxCategory == gst.CatTaxable && in.TaxRateBP == 0 && gst.ValidRate(*h.RateBP, s.Now()) {
			in.TaxRateBP, in.CessRateBP = *h.RateBP, h.CessBP
		}
		return
	}
	if in.TaxCategory != gst.CatTaxable || in.TaxRateBP != 0 {
		return
	}
	sg, err := s.SuggestHSN(ctx, tenantID, "", code, "", 1)
	if err != nil {
		return
	}
	for _, h := range sg.Suggestions {
		if h.Code == code && h.RateBP != nil && gst.ValidRate(*h.RateBP, s.Now()) {
			in.TaxRateBP, in.CessRateBP = *h.RateBP, h.CessBP
		}
	}
}

// significantWords splits a name into lower-case words of 3+ letters,
// dropping sizes and pack counts ("100g", "1kg", "x12").
func significantWords(name string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(name), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127)
	}) {
		if len([]rune(w)) < 3 || strings.IndexFunc(w, func(r rune) bool { return r >= '0' && r <= '9' }) >= 0 {
			continue
		}
		out = append(out, w)
		if len(out) == 4 {
			break
		}
	}
	return out
}
