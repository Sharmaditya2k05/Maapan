package ask

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAnnotateAddsRupees(t *testing.T) {
	out, err := Annotate([]byte(`{"sales_paise":12345678,"items":[{"value_paise":99,"name":"x"}],"bills":3}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"sales_inr":"₹1,23,456.78"`, `"value_inr":"₹0.99"`, `"bills":3`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
}

func TestAnnotateTruncates(t *testing.T) {
	out, _ := Annotate([]byte(`{"a":"`+strings.Repeat("x", 100)+`"}`), 20)
	if !strings.Contains(out, "truncated") {
		t.Fatal("expected truncation note")
	}
}

func TestExecutorValidatesInput(t *testing.T) {
	x := &Executor{Tenant: "t"}
	if _, err := x.Run(context.Background(), "get_books_summary", json.RawMessage(`{"from":"yesterday","to":"2026-10-01"}`)); err == nil {
		t.Fatal("bad date must fail")
	}
	if _, err := x.Run(context.Background(), "get_books_summary", json.RawMessage(`{"from":"2026-10-01","to":"2026-10-0`)); err == nil ||
		!strings.Contains(err.Error(), "INVALID_JSON") {
		t.Fatalf("truncated input must be INVALID_JSON, got %v", err)
	}
	if _, err := x.Run(context.Background(), "drop_tables", json.RawMessage(`{}`)); err == nil {
		t.Fatal("unknown tool must fail")
	}
}

func TestStoreScoping(t *testing.T) {
	x := &Executor{Stores: []string{"s1"}}
	if s, err := x.store(""); err != nil || s != "s1" {
		t.Fatalf("default store: %v %v", s, err)
	}
	if _, err := x.store("s2"); err == nil {
		t.Fatal("other store must be refused")
	}
	if s, _ := (&Executor{}).store(""); s != "all" {
		t.Fatal("owner sees all")
	}
}

func TestPrompt(t *testing.T) {
	p := Prompt("  aaj ki sale kitni hai? ", time.Date(2026, 10, 16, 20, 0, 0, 0, time.UTC), nil)
	if !strings.Contains(p, "2026-10-17") || !strings.Contains(p, "aaj ki sale kitni hai?") {
		t.Fatal(p)
	}
}
