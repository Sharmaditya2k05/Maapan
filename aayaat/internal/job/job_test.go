package job

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/taraju/aayaat/internal/mapping"
	"github.com/taraju/mool/auth"
)

func TestRoutesRegisterWithoutConflicts(t *testing.T) {
	mux := http.NewServeMux()
	(&Service{}).Routes(mux, auth.NewVerifier("secret-secret-secret-secret-secret", "svc"))
	// Unauthenticated calls are rejected by the verifier, proving the route exists.
	for _, p := range []string{"/v1/imports/templates/items", "/v1/imports/abc/preview", "/v1/imports/abc"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s → %d", p, rec.Code)
		}
	}
}

func TestTemplateCSV(t *testing.T) {
	rec := httptest.NewRecorder()
	if err := template(rec, "parties"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "Party Name,Party Code,Group") {
		t.Fatalf("%q", rec.Body.String())
	}
	if template(httptest.NewRecorder(), "nope") == nil {
		t.Fatal("unknown kind must 404")
	}
}

func TestErrorsCSV(t *testing.T) {
	var buf bytes.Buffer
	_ = WriteErrorsCSV(&buf, []RowResult{{RowNo: 7, Status: "invalid", Raw: map[string]string{"name": "X", "gstin": "BAD"}, Errors: []string{"gstin: bad", "dup"}}})
	want := "row,status,errors,gstin,name\n7,invalid,gstin: bad; dup,BAD,X\n"
	if buf.String() != want {
		t.Fatalf("%q", buf.String())
	}
}

func TestMappingInputAndDefaults(t *testing.T) {
	in := MappingInput{Mapping: map[string]string{"name": "Party Name", "bogus": "x"}}
	if in.ValidateFor(mapping.KindParties) == nil {
		t.Fatal("unknown field must fail")
	}
	in = MappingInput{Mapping: map[string]string{"gstin": "GSTIN"}}
	if in.ValidateFor(mapping.KindParties) == nil {
		t.Fatal("missing required name must fail")
	}
	in = MappingInput{Mapping: map[string]string{"name": "Party Name"}, Params: &Params{AsOf: "2026-04-01"}}
	if err := in.ValidateFor(mapping.KindParties); err != nil {
		t.Fatal(err)
	}
	if got := defaultAsOf(time.Date(2027, 2, 10, 0, 0, 0, 0, time.UTC)); got != "2026-04-01" {
		t.Fatalf("FY start %s", got)
	}
	if r := reconciliation(mapping.KindParties, 500, 300); r.DifferencePaise != 200 || r.Balanced {
		t.Fatalf("%+v", r)
	}
	if reconciliation(mapping.KindItems, 1, 1) != nil {
		t.Fatal("items have no reconciliation")
	}
	if sanitize("../My Parties (2026).xlsx") != "My_Parties__2026_.xlsx" {
		t.Fatal(sanitize("../My Parties (2026).xlsx"))
	}
}
