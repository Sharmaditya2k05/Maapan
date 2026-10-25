package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// MSG91 sends SMS through the MSG91 Flow API (DLT-approved templates).
// Each Maapan template maps to a flow template_id via MSG91_TEMPLATE_<NAME>;
// template variables are passed by name, so the flow must use ##otp##, ##minutes## ...
type MSG91 struct {
	authKey   string
	baseURL   string
	templates map[string]string
	http      *http.Client
}

// NewMSG91 builds the client.
func NewMSG91(cfg Config) *MSG91 {
	base := cfg.MSG91BaseURL
	if base == "" {
		base = "https://control.msg91.com"
	}
	return &MSG91{authKey: cfg.MSG91AuthKey, baseURL: strings.TrimRight(base, "/"), templates: cfg.MSG91Templates,
		http: &http.Client{Timeout: 10 * time.Second}}
}

func (m *MSG91) Name() string { return "msg91" }

func (m *MSG91) Send(ctx context.Context, msg Message) (string, error) {
	tid := m.templates[msg.Template]
	if tid == "" {
		return "", fmt.Errorf("%w: no MSG91 template id for %q", ErrNotConfigured, msg.Template)
	}
	recipient := map[string]string{"mobiles": digits(msg.To)}
	for k, v := range msg.Params {
		recipient[k] = v
	}
	body, _ := json.Marshal(map[string]any{
		"template_id": tid,
		"short_url":   "0",
		"recipients":  []map[string]string{recipient},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/api/v5/flow", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("authkey", m.authKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := m.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("msg91: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 16<<10))
	var out struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &out)
	if res.StatusCode >= 300 || out.Type != "success" {
		return "", fmt.Errorf("msg91: status %d: %s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	return out.Message, nil // MSG91 returns the request id in "message"
}
