package invoice

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// PDFLinkTTL is how long a signed public PDF link stays valid: long enough for
// Gupshup / the mailer to fetch the document (and a retry or two), short
// enough that a forwarded link dies quickly.
const PDFLinkTTL = 30 * time.Minute

// MaxPDFLinkTTL caps the ttl a caller may ask for.
const MaxPDFLinkTTL = 24 * time.Hour

// PDFLinkKey derives the link-signing key from SERVICE_SECRET, so the raw
// service secret never signs anything that leaves the platform and rotating
// the secret invalidates every outstanding link.
func PDFLinkKey(serviceSecret string) []byte {
	m := hmac.New(sha256.New, []byte(serviceSecret))
	m.Write([]byte("maapan/aadesh/invoice-pdf-link/v1"))
	return m.Sum(nil)
}

func pdfSig(key []byte, invoiceID string, exp int64) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(strings.ToLower(invoiceID) + "|" + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// SignPDFLink returns the query (exp, sig) that opens one invoice's PDF until exp.
func SignPDFLink(key []byte, invoiceID string, exp time.Time) url.Values {
	e := exp.Unix()
	return url.Values{"exp": {strconv.FormatInt(e, 10)}, "sig": {pdfSig(key, invoiceID, e)}}
}

// VerifyPDFLink checks a signed link: right invoice, untampered, not expired,
// and not dated absurdly far ahead (a leaked key would otherwise mint forever links).
func VerifyPDFLink(key []byte, invoiceID, exp, sig string, now time.Time) bool {
	if len(key) == 0 || exp == "" || sig == "" {
		return false
	}
	e, err := strconv.ParseInt(exp, 10, 64)
	if err != nil || now.Unix() > e || e > now.Add(MaxPDFLinkTTL+time.Minute).Unix() {
		return false
	}
	want := pdfSig(key, invoiceID, e)
	return hmac.Equal([]byte(want), []byte(sig))
}

// PDFLink is the answer of GET /internal/v1/invoices/{id}/pdf-link.
type PDFLink struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
	FileName  string    `json:"file_name"`
	Number    string    `json:"number"`
}

// pdfLinkURL builds the public URL of an invoice PDF.
func (s *Service) pdfLinkURL(invoiceID string, exp time.Time) string {
	base := strings.TrimRight(s.PublicBaseURL, "/")
	return base + "/public/v1/invoices/" + url.PathEscape(invoiceID) + "/pdf?" + SignPDFLink(s.PDFKey, invoiceID, exp).Encode()
}
