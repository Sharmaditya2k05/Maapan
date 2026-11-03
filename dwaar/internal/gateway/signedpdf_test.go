package gateway

import "testing"

func TestSignedInvoicePDFIsPublic(t *testing.T) {
	cases := map[string]bool{
		"GET /api/aadesh/public/v1/invoices/7f1c/pdf":        true,
		"GET /api/aadesh/public/v1/invoices/7f1c/pdf/":       true,
		"POST /api/aadesh/public/v1/invoices/7f1c/pdf":       false,
		"GET /api/aadesh/public/v1/invoices//pdf":            false,
		"GET /api/aadesh/public/v1/invoices/a/b/pdf":         false,
		"GET /api/aadesh/public/v1/invoices/../internal/pdf": false,
		"GET /api/aadesh/v1/invoices/7f1c/pdf":               false,
		"GET /api/kar/public/v1/invoices/7f1c/pdf":           false,
	}
	for in, want := range cases {
		var method, path string
		for i := range in {
			if in[i] == ' ' {
				method, path = in[:i], in[i+1:]
				break
			}
		}
		if got := IsPublic(method, path); got != want {
			t.Errorf("%s: public=%v want %v", in, got, want)
		}
	}
}
