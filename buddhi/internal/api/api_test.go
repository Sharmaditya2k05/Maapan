package api

import (
	"errors"
	"net/http"
	"testing"

	"github.com/maapan/buddhi/internal/llm"
	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/httpx"
)

func TestAIErrorMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{llm.ErrDisabled, http.StatusServiceUnavailable},
		{&llm.RefusalError{Category: "cyber"}, http.StatusUnprocessableEntity},
		{&llm.UpstreamError{Status: 429, Retryable: true}, http.StatusServiceUnavailable},
		{&llm.UpstreamError{Status: 400}, http.StatusBadGateway},
	}
	for _, c := range cases {
		if got := httpx.AsError(aiError(c.err)).Status; got != c.want {
			t.Errorf("%v -> %d, want %d", c.err, got, c.want)
		}
	}
	plain := errors.New("db down")
	if !errors.Is(aiError(plain), plain) {
		t.Fatal("other errors must pass through")
	}
}

func TestStoreScope(t *testing.T) {
	cashier := &auth.Principal{StoreID: "s1", Claims: &auth.Claims{Role: auth.RoleCashier, Stores: []string{"s1"}}}
	owner := &auth.Principal{StoreID: "s1", Claims: &auth.Claims{Role: auth.RoleSuperAdmin}}
	if s, err := storeScope(cashier, "", true); err != nil || s != "s1" {
		t.Fatalf("cashier default: %s %v", s, err)
	}
	if _, err := storeScope(cashier, "all", true); err == nil {
		t.Fatal("cashier cannot see all stores")
	}
	if _, err := storeScope(cashier, "s2", true); err == nil {
		t.Fatal("cashier cannot see other store")
	}
	if s, _ := storeScope(owner, "", true); s != "all" {
		t.Fatalf("owner default should be all, got %s", s)
	}
	if s, _ := storeScope(owner, "", false); s != "s1" {
		t.Fatalf("owner store-required default: %s", s)
	}
}
