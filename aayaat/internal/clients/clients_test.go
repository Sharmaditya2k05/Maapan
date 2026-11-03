package clients

import (
	"encoding/json"
	"testing"

	"github.com/maapan/mool/httpx"
)

func TestShapeKeepsAcceptedFields(t *testing.T) {
	rec := json.RawMessage(`{"name":"Ram","pan":"ABCPG7310Q","shipping_address":"x","store_id":"s","opening_as_of":"2026-04-01"}`)
	out, err := Shape(PathParties, rec, false)
	if err != nil || string(out) != `{"name":"Ram","opening_as_of":"2026-04-01"}` {
		t.Fatalf("%s %v", out, err)
	}
	out, _ = Shape(PathParties, rec, true)
	if string(out) != `{"name":"Ram","opening_as_of":"2026-04-01","pan":"ABCPG7310Q","shipping_address":"x"}` {
		t.Fatalf("extras: %s", out)
	}
	if out, _ := Shape(PathOpenBills, rec, false); string(out) != string(rec) {
		t.Fatal("designed endpoints get the record as is")
	}
	tg := Targets{BahiExtras: true}
	if !tg.ExtrasFor(PathParties) || tg.ExtrasFor(PathItems) {
		t.Fatal("extras per service")
	}
}

func TestIsMissingEndpoint(t *testing.T) {
	if !IsMissingEndpoint(httpx.Upstream("POST /internal/v1/bulk/open-bills returned 404")) || !IsMissingEndpoint(httpx.NotFound("x")) {
		t.Fatal("404s are missing endpoints")
	}
	if IsMissingEndpoint(httpx.Validation("bad row")) || IsMissingEndpoint(nil) {
		t.Fatal("422 is not")
	}
}
