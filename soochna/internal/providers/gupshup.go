package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/maapan/soochna/internal/templates"
)

// Gupshup sends WhatsApp messages through the Gupshup WhatsApp API.
//
// Business-initiated messages (bills, alerts, OTPs) must use a Meta-approved
// template: set GUPSHUP_TEMPLATE_<NAME>=<template id>; variables are sent in
// templates.ParamOrder. Without a template id the rendered body is sent as a
// free-form text message, which WhatsApp only delivers inside a 24-hour
// customer-service window (fine for local testing on your own number).
type Gupshup struct {
	apiKey    string
	source    string
	appName   string
	baseURL   string
	templates map[string]string
	http      *http.Client
}

// NewGupshup builds the client.
func NewGupshup(cfg Config) *Gupshup {
	base := cfg.GupshupBaseURL
	if base == "" {
		base = "https://api.gupshup.io"
	}
	return &Gupshup{apiKey: cfg.GupshupAPIKey, source: digits(cfg.GupshupSource), appName: cfg.GupshupAppName,
		baseURL: strings.TrimRight(base, "/"), templates: cfg.GupshupTemplates, http: &http.Client{Timeout: 10 * time.Second}}
}

func (g *Gupshup) Name() string { return "gupshup" }

func (g *Gupshup) Send(ctx context.Context, m Message) (string, error) {
	form := url.Values{}
	form.Set("channel", "whatsapp")
	source, appName := g.source, g.appName
	if m.SenderID != "" { // per-tenant WhatsApp number from the registry
		source = digits(m.SenderID)
		if m.AppName != "" {
			appName = m.AppName
		}
	}
	form.Set("source", source)
	form.Set("destination", digits(m.To))
	form.Set("src.name", appName)

	tid := m.TemplateID
	if tid == "" {
		tid = g.templates[m.Template]
	}
	endpoint := "/wa/api/v1/msg"
	name := fileName(m.MediaFilename, m.MediaURL)
	if tid != "" {
		endpoint = "/wa/api/v1/template/msg"
		tpl, _ := json.Marshal(map[string]any{"id": tid, "params": templates.Positional(m.Template, m.Params)})
		form.Set("template", string(tpl))
		if m.MediaURL != "" { // template with a DOCUMENT header
			doc, _ := json.Marshal(map[string]any{"type": "document", "document": map[string]string{
				"link": m.MediaURL, "filename": name}})
			form.Set("message", string(doc))
		}
	} else {
		var msg []byte
		if m.MediaURL != "" {
			msg, _ = json.Marshal(map[string]string{"type": "file", "url": m.MediaURL, "filename": name, "caption": m.Body})
		} else {
			msg, _ = json.Marshal(map[string]string{"type": "text", "text": m.Body})
		}
		form.Set("message", string(msg))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("apikey", g.apiKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Cache-Control", "no-cache")
	res, err := g.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("gupshup: %w", err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 16<<10))
	var out struct {
		Status    string `json:"status"`
		MessageID string `json:"messageId"`
		Message   string `json:"message"`
	}
	_ = json.Unmarshal(raw, &out)
	if res.StatusCode >= 300 || out.Status != "submitted" {
		return "", fmt.Errorf("gupshup: status %d: %s", res.StatusCode, strings.TrimSpace(string(raw)))
	}
	return out.MessageID, nil
}
