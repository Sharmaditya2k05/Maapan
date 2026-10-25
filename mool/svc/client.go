// Package svc is the HTTP client for internal service-to-service calls.
// It adds X-Service-Secret, tenant/store/request headers and, on Cloud Run,
// a Google ID token for the callee.
package svc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/maapan/mool/auth"
	"github.com/maapan/mool/config"
	"github.com/maapan/mool/gcp"
	"github.com/maapan/mool/httpx"
	"github.com/maapan/mool/logx"
)

// Client calls one downstream service.
type Client struct {
	BaseURL       string
	ServiceSecret string
	UseIDToken    bool
	HTTP          *http.Client
}

// New builds a client for baseURL (e.g. BAHI_URL).
func New(baseURL, serviceSecret string, useIDToken bool) *Client {
	return &Client{
		BaseURL:       strings.TrimRight(baseURL, "/"),
		ServiceSecret: serviceSecret,
		UseIDToken:    useIDToken,
		HTTP:          &http.Client{Timeout: config.ServiceTimeout()},
	}
}

// WithRequestID stores the inbound request ID so outbound calls carry it.
// The httpx.RequestID middleware already does this for every request, so
// handlers never need to call it; it remains for jobs and tests.
func WithRequestID(ctx context.Context, id string) context.Context {
	return httpx.WithRequestID(ctx, id)
}

// Do sends method path with body (JSON-encoded unless nil) and decodes into out (unless nil).
// tenantID/storeID come from the caller's principal when present in ctx.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	if c == nil || c.BaseURL == "" {
		return httpx.Unavailable("downstream service URL not configured")
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set(auth.HeaderServiceSecret, c.ServiceSecret)
	if p := auth.FromContext(ctx); p != nil {
		req.Header.Set(auth.HeaderTenantID, p.TenantID)
		if p.StoreID != "" {
			req.Header.Set(auth.HeaderStoreID, p.StoreID)
		}
		req.Header.Set(auth.HeaderUserID, p.UserID())
	}
	if id := httpx.RequestIDFrom(ctx); id != "" {
		req.Header.Set(httpx.HeaderRequestID, id)
	}
	if c.UseIDToken {
		u, _ := url.Parse(c.BaseURL)
		tok, err := gcp.IDToken(ctx, u.Scheme+"://"+u.Host)
		if err != nil {
			return httpx.Upstream("id token: %s", err.Error())
		}
		// Cloud Run checks this header for IAM, leaving Authorization free for a user token.
		req.Header.Set("X-Serverless-Authorization", "Bearer "+tok)
	}

	res, err := c.HTTP.Do(req)
	if err != nil {
		logx.From(ctx).Warn("downstream call failed", "url", c.BaseURL+path, "error", err.Error())
		return httpx.Upstream("call %s failed", path)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode >= 400 {
		var env struct {
			Error *httpx.Error `json:"error"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Error != nil {
			env.Error.Status = res.StatusCode
			return env.Error
		}
		return httpx.Upstream("%s %s returned %d", method, path, res.StatusCode)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return nil
}

// Get is Do with GET.
func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.Do(ctx, http.MethodGet, path, nil, out)
}

// Post is Do with POST.
func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	return c.Do(ctx, http.MethodPost, path, body, out)
}
