package job

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/maapan/aayaat/internal/mapping"
	"github.com/maapan/aayaat/internal/storage"
	"github.com/maapan/mool/auth"
)

func TestRoutesRegisterWithoutConflicts(t *testing.T) {
	mux := http.NewServeMux()
	(&Service{}).Routes(mux, auth.NewVerifier("secret-secret-secret-secret-secret", "svc"))
	// Unauthenticated calls are rejected by the verifier, proving the route exists.
	for _, r := range []struct{ method, path string }{
		{"GET", "/v1/imports/templates/items"}, {"GET", "/v1/imports/abc/preview"}, {"GET", "/v1/imports/abc"},
		{"GET", "/v1/imports/abc/batches"}, {"POST", "/v1/imports/abc/dry-run"}, {"POST", "/v1/imports/abc/rollback"},
		{"GET", "/v1/mapping-templates"}, {"POST", "/v1/mapping-templates"}, {"DELETE", "/v1/mapping-templates/x"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(r.method, r.path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s → %d", r.method, r.path, rec.Code)
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

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// An oversize upload is rejected and its stored object removed, never orphaned.
func TestCreateRemovesOversizeUpload(t *testing.T) {
	dir := t.TempDir()
	s := &Service{Store: storage.Local{Dir: dir}, Log: slog.Default(), Now: time.Now}
	p := &auth.Principal{TenantID: "t1"}
	_, err := s.Create(context.Background(), p, "parties", "huge.csv", "text/csv", io.LimitReader(zeros{}, MaxFileBytes+10), Params{})
	if err == nil || !strings.Contains(err.Error(), "larger than 50 MB") {
		t.Fatalf("err %v", err)
	}
	var files int
	_ = walk(dir, func() { files++ })
	if files != 0 {
		t.Fatalf("%d files left behind", files)
	}
	if _, err := s.Create(context.Background(), p, "ledgers", "x.csv", "", strings.NewReader("a"), Params{}); err == nil {
		t.Fatal("unknown kind")
	}
	if _, err := s.Create(context.Background(), p, "parties", "x.pdf", "", strings.NewReader("a"), Params{}); err == nil {
		t.Fatal("unknown extension")
	}
	if _, err := s.Create(context.Background(), p, "open_bills", "x.csv", "", strings.NewReader("a"), Params{BillsMode: "weekly"}); err == nil {
		t.Fatal("bad bills_mode")
	}
}

func walk(dir string, fn func()) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			_ = walk(dir+"/"+e.Name(), fn)
			continue
		}
		fn()
	}
	return nil
}
