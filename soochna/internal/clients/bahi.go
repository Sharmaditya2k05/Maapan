package clients

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/svc"
)

// Party is bahi's internal party shape (GET /internal/v1/parties/{id}).
type Party struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Phone        string `json:"phone"`
	Email        string `json:"email"`
	CreditDays   int    `json:"credit_days"`
	BalancePaise int64  `json:"balance_paise"`
}

// OverdueParty is one row of bahi's overdue-receivables list
// (GET /internal/v1/receivables/overdue?as_of=YYYY-MM-DD).
type OverdueParty struct {
	PartyID       string `json:"party_id"`
	PartyName     string `json:"party_name"`
	Phone         string `json:"phone"`
	Email         string `json:"email"`
	OverduePaise  int64  `json:"overdue_paise"`   // sum of bills past their due date
	OldestDueDate string `json:"oldest_due_date"` // YYYY-MM-DD of the oldest unpaid due bill
	DaysOverdue   int    `json:"days_overdue"`    // as_of − oldest_due_date
	PayLink       string `json:"pay_link,omitempty"`
}

// ErrOverdueUnsupported means bahi does not expose the overdue endpoint yet.
var ErrOverdueUnsupported = httpx.Unavailable("bahi has no GET /internal/v1/receivables/overdue yet")

// BahiClient calls bahi.
type BahiClient struct{ C *svc.Client }

func tenantCtx(ctx context.Context, tenantID string) context.Context {
	return auth.WithPrincipal(ctx, &auth.Principal{Internal: true, TenantID: tenantID})
}

// Party returns one party, or (nil, nil) when bahi does not know it.
func (b *BahiClient) Party(ctx context.Context, tenantID, partyID string) (*Party, error) {
	var p Party
	err := b.C.Get(tenantCtx(ctx, tenantID), "/internal/v1/parties/"+url.PathEscape(partyID), &p)
	if e := httpx.AsError(err); err != nil && e.Status == http.StatusNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Overdue lists parties with bills past their due date as of asOf.
func (b *BahiClient) Overdue(ctx context.Context, tenantID, asOf string) ([]OverdueParty, error) {
	var out struct {
		Rows []OverdueParty `json:"rows"`
	}
	err := b.C.Get(tenantCtx(ctx, tenantID), "/internal/v1/receivables/overdue?as_of="+url.QueryEscape(asOf), &out)
	if err != nil {
		// The Go mux answers an unknown route with a plain-text 404, which
		// svc.Client reports as "GET … returned 404".
		if e := httpx.AsError(err); strings.Contains(e.Message, "returned 404") {
			return nil, ErrOverdueUnsupported
		}
		return nil, err
	}
	return out.Rows, nil
}
