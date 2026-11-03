package clients

import (
	"context"
	"net/url"

	"github.com/maapan/mool/svc"
)

// AadeshPDF fetches signed invoice-PDF links from aadesh
// (GET /internal/v1/invoices/{id}/pdf-link). The link is public but signed
// and short-lived, so WhatsApp and the e-mail provider can download it.
type AadeshPDF struct {
	C *svc.Client
	// TTLSeconds is how long a link stays valid (aadesh caps it at 24 h).
	TTLSeconds int
}

type pdfLink struct {
	URL      string `json:"url"`
	FileName string `json:"file_name"`
}

// Link returns a fresh signed URL and file name for an invoice's PDF.
func (a *AadeshPDF) Link(ctx context.Context, tenantID, invoiceID string) (string, string, error) {
	ttl := a.TTLSeconds
	if ttl <= 0 {
		ttl = 86400
	}
	var out pdfLink
	path := "/internal/v1/invoices/" + url.PathEscape(invoiceID) + "/pdf-link?ttl=" + url.QueryEscape(itoa(ttl))
	if err := a.C.Get(svc.ForTenant(ctx, tenantID), path, &out); err != nil {
		return "", "", err
	}
	return out.URL, out.FileName, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
