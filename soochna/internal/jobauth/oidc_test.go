package jobauth

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/maapan/mool/auth"
)

func TestSchedulerOIDC(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	fetches := 0
	certs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fetches++
		w.Header().Set("Cache-Control", "public, max-age=3600")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	defer certs.Close()

	const aud = "https://soochna-1.asia-south1.run.app"
	const sa = "scheduler@maapan-dev.iam.gserviceaccount.com"
	o := &OIDC{Audience: aud, Emails: []string{sa}, CertsURL: certs.URL}
	sign := func(k *rsa.PrivateKey, kid, iss, audience, email string, exp time.Duration) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
			"iss": iss, "aud": audience, "email": email, "email_verified": true,
			"iat": time.Now().Unix(), "exp": time.Now().Add(exp).Unix(),
		})
		tok.Header["kid"] = kid
		s, err := tok.SignedString(k)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	v := auth.NewVerifier("jwt", "svc-secret")
	h := Middleware(v, o)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := auth.FromContext(r.Context()); p == nil || !p.Internal {
			t.Error("principal must be internal")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	call := func(hdr, val string) int {
		req := httptest.NewRequest(http.MethodPost, "/internal/v1/jobs/retry-failed", nil)
		if hdr != "" {
			req.Header.Set(hdr, val)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	good := sign(key, "k1", "https://accounts.google.com", aud, sa, time.Hour)
	cases := []struct {
		name, hdr, val string
		want           int
	}{
		{"scheduler token", "Authorization", "Bearer " + good, 204},
		{"service secret still works", auth.HeaderServiceSecret, "svc-secret", 204},
		{"nothing", "", "", 401},
		{"wrong audience", "Authorization", "Bearer " + sign(key, "k1", "https://accounts.google.com", "https://evil", sa, time.Hour), 401},
		{"wrong account", "Authorization", "Bearer " + sign(key, "k1", "accounts.google.com", aud, "dwaar@x.iam.gserviceaccount.com", time.Hour), 401},
		{"wrong issuer", "Authorization", "Bearer " + sign(key, "k1", "https://evil.example", aud, sa, time.Hour), 401},
		{"expired", "Authorization", "Bearer " + sign(key, "k1", "https://accounts.google.com", aud, sa, -time.Hour), 401},
		{"forged signature", "Authorization", "Bearer " + sign(other, "k1", "https://accounts.google.com", aud, sa, time.Hour), 401},
		{"unknown key", "Authorization", "Bearer " + sign(key, "k9", "https://accounts.google.com", aud, sa, time.Hour), 401},
	}
	for _, c := range cases {
		if got := call(c.hdr, c.val); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
	if fetches > 2 { // first fetch + one refetch for the unknown kid
		t.Errorf("keys fetched %d times; cache not used", fetches)
	}

	// Without OIDC config only the secret is accepted.
	h2 := Middleware(v, &OIDC{})(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) }))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer "+good)
	rec := httptest.NewRecorder()
	h2.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatalf("OIDC disabled must reject tokens, got %d", rec.Code)
	}
}

func TestMaxAge(t *testing.T) {
	if maxAge("public, max-age=19800, must-revalidate") != 19800*time.Second || maxAge("") != time.Hour || maxAge("max-age=999999") != 24*time.Hour {
		t.Fatal("maxAge")
	}
}
