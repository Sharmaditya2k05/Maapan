// Package gcp talks to the Cloud Run metadata server for access and ID tokens,
// so services need no Google SDK or key files.
package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const metadataBase = "http://metadata.google.internal/computeMetadata/v1"

var client = &http.Client{Timeout: 3 * time.Second}

func get(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataBase+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("metadata server: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metadata %s: %d %s", path, res.StatusCode, strings.TrimSpace(string(b)))
	}
	return b, nil
}

type cached struct {
	token string
	exp   time.Time
}

var (
	mu       sync.Mutex
	access   cached
	idTokens = map[string]cached{}
)

// AccessToken returns an OAuth access token for the runtime service account.
func AccessToken(ctx context.Context) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if access.token != "" && time.Until(access.exp) > time.Minute {
		return access.token, nil
	}
	b, err := get(ctx, "/instance/service-accounts/default/token")
	if err != nil {
		return "", err
	}
	var t struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(b, &t); err != nil {
		return "", err
	}
	access = cached{t.AccessToken, time.Now().Add(time.Duration(t.ExpiresIn) * time.Second)}
	return t.AccessToken, nil
}

// IDToken returns a Google-signed ID token whose audience is the callee URL
// (what Cloud Run IAM auth checks). Tokens are cached for 50 minutes.
func IDToken(ctx context.Context, audience string) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	if c, ok := idTokens[audience]; ok && time.Until(c.exp) > time.Minute {
		return c.token, nil
	}
	b, err := get(ctx, "/instance/service-accounts/default/identity?audience="+url.QueryEscape(audience))
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	idTokens[audience] = cached{tok, time.Now().Add(50 * time.Minute)}
	return tok, nil
}
