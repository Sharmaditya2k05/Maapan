package gsp

import (
	"encoding/json"
	"strings"

	"github.com/taraju/mool/gst"
)

type taxpayerJSON struct {
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

// ParseTaxpayer maps a GSTN "search taxpayer" payload (top level or wrapped
// under data / result / taxpayerInfo) to a Taxpayer.
func ParseTaxpayer(g string, body []byte) (Taxpayer, error) {
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal(body, &wrapper); err != nil {
		return Taxpayer{}, errf(KindUnavailable, CodeProviderDown, "", "gsp search response: %s", err.Error())
	}
	raw := json.RawMessage(body)
	for _, k := range []string{"data", "result", "taxpayerInfo"} {
		if v, ok := wrapper[k]; ok && len(v) > 0 && v[0] == '{' {
			raw = v
			break
		}
	}
	var tp taxpayerJSON
	if err := json.Unmarshal(raw, &tp); err != nil {
		return Taxpayer{}, errf(KindUnavailable, CodeProviderDown, "", "gsp search response: %s", err.Error())
	}
	if tp.Lgnm == "" && tp.TradeNam == "" {
		msg := errorMessage(wrapper)
		if msg == "" {
			msg = "taxpayer not found"
		}
		return Taxpayer{}, errf(KindNotFound, "", "gstin", "%s", msg)
	}
	g = gst.Normalize(g)
	a := tp.Pradr.Addr
	addr := tp.Pradr.Adr
	if addr == "" {
		addr = joinNonEmpty(", ", a.Bno, a.Flno, a.Bnm, a.St, a.Loc, a.City, a.Dst, a.Stcd, a.Pncd)
	}
	return Taxpayer{GSTIN: g, LegalName: tp.Lgnm, TradeName: tp.TradeNam, Status: NormalizeStatus(tp.Sts),
		RegistrationType: tp.Dty, StateCode: gst.StateCode(g), Address: addr}, nil
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

// NormalizeStatus maps portal status strings to Active / Cancelled / Suspended / Inactive.
func NormalizeStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "active", "act":
		return "Active"
	case "cancelled", "canceled", "cnl":
		return "Cancelled"
	case "suspended", "sus":
		return "Suspended"
	case "inactive", "ina":
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
