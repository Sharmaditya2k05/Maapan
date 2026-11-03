package svc

import (
	"context"
	"testing"

	"github.com/maapan/mool/auth"
)

func TestForTenant(t *testing.T) {
	if p := auth.FromContext(ForTenant(context.Background(), "t9")); p == nil || p.TenantID != "t9" || !p.Internal {
		t.Fatalf("job context must act for the tenant: %+v", p)
	}
	ctx := auth.WithPrincipal(context.Background(), &auth.Principal{TenantID: "t1"})
	if p := auth.FromContext(ForTenant(ctx, "t9")); p.TenantID != "t1" {
		t.Fatalf("an existing tenant must win: %+v", p)
	}
}
