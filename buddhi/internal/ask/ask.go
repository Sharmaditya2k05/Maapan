// Package ask is "ask your books": a Claude agent with a small set of
// read-only tools over the tenant's own data. Questions may be in Hindi,
// English or Hinglish; answers come back in the same language.
package ask

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/maapan/buddhi/internal/clients"
	"github.com/maapan/buddhi/internal/kpi"
	"github.com/maapan/buddhi/internal/llm"
	"github.com/maapan/mool/money"
)

// MaxToolCalls caps the agent loop.
const MaxToolCalls = 6

// maxToolResultBytes keeps tool results (and token spend) bounded.
const maxToolResultBytes = 24 << 10

// System is stable (prompt-cached); per-request facts go in the user prompt.
const System = `You are Maapan's books assistant for an Indian retail business. You answer the owner's or accountant's questions about their own sales, purchases, stock, dues and GST using only the tools provided.

How to answer:
- Reply in the language and script of the question (Hindi, English or Hinglish).
- Use the tools to get numbers; never guess or invent figures. If the tools can't answer, say what is missing.
- Amounts in tool results come as paise integers with a ready-formatted "<field>_inr" companion — quote the _inr value (Indian grouping, e.g. ₹1,23,456.00). Never convert paise yourself.
- Dates are in India time. "This month" means from the 1st of the current month to today; the financial year starts on 1 April.
- Be brief: lead with the answer, then at most a few supporting lines or a short list.
- You can only read data. If asked to change something (create a bill, edit stock), explain where in the app to do it.`

// Tools are the read-only tools offered to Claude.
var Tools = []llm.Tool{
	{
		Name:        "get_books_summary",
		Description: "Accounting summary for a date range from the ledger: total sales, purchases, receivables (customers owe us), payables (we owe suppliers), cash and bank balances.",
		Properties: map[string]any{
			"from": map[string]any{"type": "string", "description": "Start date YYYY-MM-DD"},
			"to":   map[string]any{"type": "string", "description": "End date YYYY-MM-DD"},
		},
		Required: []string{"from", "to"},
	},
	{
		Name:        "get_sales_kpis",
		Description: "Point-of-sale KPIs for a date range: sales, bills, average bill, GST collected, gross margin, cancellations and the top 10 items. Optionally for one store.",
		Properties: map[string]any{
			"from":     map[string]any{"type": "string", "description": "Start date YYYY-MM-DD"},
			"to":       map[string]any{"type": "string", "description": "End date YYYY-MM-DD"},
			"store_id": map[string]any{"type": "string", "description": "Store id, or empty for the whole business"},
		},
		Required: []string{"from", "to", "store_id"},
	},
	{
		Name:        "get_stock_summary",
		Description: "Current stock per item: quantity on hand, stock value and re-order level. Optionally for one store.",
		Properties: map[string]any{
			"store_id": map[string]any{"type": "string", "description": "Store id, or empty for all stores"},
		},
		Required: []string{"store_id"},
	},
	{
		Name:        "list_invoices",
		Description: "List invoices/bills (newest first, max 50) with totals and payment status. Use for questions about specific bills, unpaid bills, a customer's or supplier's bills.",
		Properties: map[string]any{
			"doc_type":       map[string]any{"type": "string", "enum": []string{"sale", "purchase", "sale_return", "purchase_return", ""}},
			"from":           map[string]any{"type": "string", "description": "Start date YYYY-MM-DD or empty"},
			"to":             map[string]any{"type": "string", "description": "End date YYYY-MM-DD or empty"},
			"payment_status": map[string]any{"type": "string", "enum": []string{"unpaid", "partial", "paid", ""}},
			"search":         map[string]any{"type": "string", "description": "Invoice number, party name or phone; empty for all"},
			"limit":          map[string]any{"type": "integer", "description": "1-50"},
		},
		Required: []string{"doc_type", "from", "to", "payment_status", "search", "limit"},
	},
}

// Executor runs the tools for one tenant.
type Executor struct {
	Clients *clients.Clients
	KPI     *kpi.Service
	Tenant  string
	Stores  []string // non-empty: the caller may only see these stores
}

type rangeIn struct {
	From    string `json:"from"`
	To      string `json:"to"`
	StoreID string `json:"store_id"`
}

type invoicesIn struct {
	DocType       string `json:"doc_type"`
	From          string `json:"from"`
	To            string `json:"to"`
	PaymentStatus string `json:"payment_status"`
	Search        string `json:"search"`
	Limit         int    `json:"limit"`
}

// Run implements llm.ToolFunc. Inputs are validated here because eager input
// streaming means the API does not validate them.
func (x *Executor) Run(ctx context.Context, name string, input json.RawMessage) (string, error) {
	var out any
	var err error
	switch name {
	case "get_books_summary":
		var in rangeIn
		if err := strictDecode(input, &in); err != nil {
			return "", err
		}
		if err := validDates(in.From, in.To, true); err != nil {
			return "", err
		}
		out, err = x.Clients.BooksSummary(ctx, in.From, in.To)
	case "get_sales_kpis":
		var in rangeIn
		if err := strictDecode(input, &in); err != nil {
			return "", err
		}
		if err := validDates(in.From, in.To, true); err != nil {
			return "", err
		}
		store, err2 := x.store(in.StoreID)
		if err2 != nil {
			return "", err2
		}
		total, top, e := x.KPI.Range(ctx, x.Tenant, store, in.From, in.To)
		out, err = map[string]any{"totals": total, "top_items": top}, e
	case "get_stock_summary":
		var in rangeIn
		if err := strictDecode(input, &in); err != nil {
			return "", err
		}
		store, err2 := x.store(in.StoreID)
		if err2 != nil {
			return "", err2
		}
		if store == kpi.AllStores {
			store = ""
		}
		out, err = x.Clients.StockSummary(ctx, store)
	case "list_invoices":
		var in invoicesIn
		if err := strictDecode(input, &in); err != nil {
			return "", err
		}
		if err := validDates(in.From, in.To, false); err != nil {
			return "", err
		}
		q := url.Values{}
		set := func(k, v string) {
			if v = strings.TrimSpace(v); v != "" {
				q.Set(k, v)
			}
		}
		set("doc_type", in.DocType)
		set("from", in.From)
		set("to", in.To)
		set("payment_status", in.PaymentStatus)
		set("q", in.Search)
		if len(x.Stores) == 1 {
			q.Set("store_id", x.Stores[0])
		}
		q.Set("limit", fmt.Sprint(min(max(in.Limit, 1), 50)))
		out, err = x.Clients.Invoices(ctx, q)
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
	if err != nil {
		return "", fmt.Errorf("%s failed: %w", name, err)
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return Annotate(b, maxToolResultBytes)
}

func (x *Executor) store(id string) (string, error) {
	id = strings.TrimSpace(id)
	if len(x.Stores) == 0 {
		if id == "" {
			return kpi.AllStores, nil
		}
		return id, nil
	}
	if id == "" && len(x.Stores) == 1 {
		return x.Stores[0], nil
	}
	for _, s := range x.Stores {
		if s == id {
			return id, nil
		}
	}
	return "", fmt.Errorf("you can only see your own store; pass store_id %q", x.Stores[0])
}

func strictDecode(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("INVALID_JSON: %v", err)
	}
	return nil
}

func validDates(from, to string, required bool) error {
	for _, d := range []string{from, to} {
		if d == "" && !required {
			continue
		}
		if _, err := time.Parse(time.DateOnly, d); err != nil {
			return fmt.Errorf("dates must be YYYY-MM-DD, got %q", d)
		}
	}
	return nil
}

// Annotate adds a "<key>_inr" string next to every "<key>_paise" number so the
// model quotes exact, Indian-grouped rupee values. Results over limit bytes are
// truncated with a note.
func Annotate(raw []byte, limit int) (string, error) {
	var v any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	v = annotate(v)
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if limit > 0 && len(b) > limit {
		return string(b[:limit]) + "\n…[truncated: narrow the date range or filters for the rest]", nil
	}
	return string(b), nil
}

func annotate(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t)*2)
		for k, val := range t {
			out[k] = annotate(val)
			if strings.HasSuffix(k, "_paise") {
				if n, ok := val.(json.Number); ok {
					if p, err := n.Int64(); err == nil {
						out[strings.TrimSuffix(k, "_paise")+"_inr"] = money.Format(p)
					}
				}
			}
		}
		return out
	case []any:
		for i := range t {
			t[i] = annotate(t[i])
		}
		return t
	}
	return v
}

// Prompt wraps the question with today's date and scope (kept out of the
// cached system prompt).
func Prompt(question string, now time.Time, stores []string) string {
	ist := now.In(time.FixedZone("IST", 5*3600+1800))
	scope := "the whole business (all stores)"
	if len(stores) > 0 {
		scope = "store(s) " + strings.Join(stores, ", ") + " only"
	}
	return fmt.Sprintf("Today is %s (%s, India time). The user can see %s.\n\nQuestion: %s",
		ist.Format(time.DateOnly), ist.Weekday(), scope, strings.TrimSpace(question))
}
