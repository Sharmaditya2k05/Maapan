package providers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestMSG91SendsFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/flow" || r.Header.Get("authkey") != "k" {
			t.Errorf("bad request %s %s", r.URL.Path, r.Header.Get("authkey"))
		}
		var body struct {
			TemplateID string              `json:"template_id"`
			Recipients []map[string]string `json:"recipients"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.TemplateID != "tpl-otp" || body.Recipients[0]["mobiles"] != "919876543210" || body.Recipients[0]["otp"] != "123456" {
			t.Errorf("bad body %+v", body)
		}
		_, _ = io.WriteString(w, `{"type":"success","message":"req-1"}`)
	}))
	defer srv.Close()
	m := NewMSG91(Config{MSG91AuthKey: "k", MSG91BaseURL: srv.URL, MSG91Templates: map[string]string{"otp": "tpl-otp"}})
	ref, err := m.Send(context.Background(), Message{Channel: "sms", To: "+919876543210", Template: "otp", Params: map[string]string{"otp": "123456"}})
	if err != nil || ref != "req-1" {
		t.Fatalf("ref=%q err=%v", ref, err)
	}
	if _, err := m.Send(context.Background(), Message{Template: "invoice"}); err == nil {
		t.Fatal("missing template id must fail")
	}
}

func TestGupshupTemplateAndText(t *testing.T) {
	var last url.Values
	var lastPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		last, lastPath = r.PostForm, r.URL.Path
		_, _ = io.WriteString(w, `{"status":"submitted","messageId":"gs-1"}`)
	}))
	defer srv.Close()
	g := NewGupshup(Config{GupshupAPIKey: "k", GupshupSource: "+919000000000", GupshupAppName: "taraju", GupshupBaseURL: srv.URL,
		GupshupTemplates: map[string]string{"otp": "wa-otp"}})

	ref, err := g.Send(context.Background(), Message{To: "+919876543210", Template: "otp", Params: map[string]string{"otp": "1", "minutes": "5"}})
	if err != nil || ref != "gs-1" || lastPath != "/wa/api/v1/template/msg" || last.Get("destination") != "919876543210" {
		t.Fatalf("template send: %v %s %v", err, lastPath, last)
	}
	if last.Get("template") != `{"id":"wa-otp","params":["1","5"]}` {
		t.Fatalf("template payload %s", last.Get("template"))
	}
	if _, err := g.Send(context.Background(), Message{To: "+919876543210", Template: "invoice", Body: "hi"}); err != nil || lastPath != "/wa/api/v1/msg" {
		t.Fatalf("text send: %v %s", err, lastPath)
	}
}

func TestBuild(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s, w, err := Build(Config{}, log)
	if err != nil || s.Name() != "console" || w.Name() != "console" {
		t.Fatal("default must be console")
	}
	if _, _, err := Build(Config{SMSProvider: "msg91"}, log); err == nil {
		t.Fatal("msg91 without key must fail")
	}
}
