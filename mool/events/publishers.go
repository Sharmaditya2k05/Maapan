package events

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/taraju/mool/auth"
	"github.com/taraju/mool/config"
	"github.com/taraju/mool/gcp"
)

// NewPublisher picks the driver from EVENTS_DRIVER: pubsub | direct | log.
func NewPublisher(cfg config.Base, log *slog.Logger) Publisher {
	switch cfg.EventsDriver {
	case "pubsub":
		return &PubSub{Project: cfg.GCPProject, http: &http.Client{Timeout: 10 * time.Second}}
	case "log":
		return &LogOnly{Log: log}
	default:
		return &Direct{Secret: cfg.ServiceSecret, Log: log, http: &http.Client{Timeout: 10 * time.Second}}
	}
}

// PubSub publishes through the Pub/Sub REST API with the metadata-server token.
type PubSub struct {
	Project string
	http    *http.Client
}

func (p *PubSub) Publish(ctx context.Context, e Envelope) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{"messages": []map[string]any{{
		"data": base64.StdEncoding.EncodeToString(data),
		"attributes": map[string]string{
			"type": e.Type, "tenant_id": e.TenantID, "source": e.Source, "event_id": e.ID,
		},
	}}})
	tok, err := gcp.AccessToken(ctx)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("https://pubsub.googleapis.com/v1/projects/%s/topics/%s:publish", p.Project, e.Type)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	res, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 4<<10))
		return fmt.Errorf("pubsub publish %s: %d %s", e.Type, res.StatusCode, b)
	}
	return nil
}

// Direct POSTs the envelope to each subscriber's /events (local development).
// Subscriber base URLs come from <SERVICE>_URL env vars, e.g. BAHI_URL.
type Direct struct {
	Secret string
	Log    *slog.Logger
	http   *http.Client
}

func (d *Direct) Publish(ctx context.Context, e Envelope) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var errs []string
	for _, sub := range Subscriptions[e.Type] {
		base := os.Getenv(strings.ToUpper(sub) + "_URL")
		if base == "" {
			d.Log.Debug("event subscriber url not set, skipping", "type", e.Type, "subscriber", sub)
			continue
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/events", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(auth.HeaderServiceSecret, d.Secret)
		res, err := d.http.Do(req)
		if err != nil {
			errs = append(errs, sub+": "+err.Error())
			continue
		}
		res.Body.Close()
		if res.StatusCode >= 300 {
			errs = append(errs, fmt.Sprintf("%s: status %d", sub, res.StatusCode))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("direct publish %s: %s", e.Type, strings.Join(errs, "; "))
	}
	return nil
}

// LogOnly logs events instead of sending them (tests, isolated runs).
type LogOnly struct{ Log *slog.Logger }

func (l *LogOnly) Publish(_ context.Context, e Envelope) error {
	l.Log.Info("event (log driver)", "type", e.Type, "id", e.ID, "tenant_id", e.TenantID)
	return nil
}
