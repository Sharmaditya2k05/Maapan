package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestSupportsServerFallback(t *testing.T) {
	for _, m := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-opus-5", "claude-fable-5-1", " Claude-Opus-5-5 "} {
		if !SupportsServerFallback(m) {
			t.Errorf("%s should support the server-side fallback", m)
		}
	}
	for _, m := range []string{"claude-haiku-5-5", "claude-haiku-4-5", "claude-sonnet-4-6", "", "gpt-x"} {
		if SupportsServerFallback(m) {
			t.Errorf("%s must not get the fallback header", m)
		}
	}
	if useFallback(false, "claude-opus-5-5") {
		t.Error("AI_REFUSAL_FALLBACK=false must turn it off")
	}
}

func TestOverBudget(t *testing.T) {
	if overBudget(Usage{InputTokens: 10}, 0) {
		t.Fatal("0 = no budget")
	}
	if !overBudget(Usage{InputTokens: 60, OutputTokens: 40}, 100) {
		t.Fatal("100 of 100 used is over")
	}
}

// fakeAPI is a local stand-in for the Messages endpoint that records what
// buddhi sent. No request ever leaves the machine.
type fakeAPI struct {
	mu     sync.Mutex
	betas  []string
	bodies []map[string]any
}

func (f *fakeAPI) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		f.mu.Lock()
		f.betas = append(f.betas, r.Header.Get("anthropic-beta"))
		f.bodies = append(f.bodies, body)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		model, _ := body["model"].(string)
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","model":"`+model+`",
			"content":[{"type":"text","text":"{\"ok\":true}"}],"stop_reason":"end_turn","stop_sequence":null,
			"usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`)
	}
}

func TestFallbackHeaderOnlyForSupportingModels(t *testing.T) {
	api := &fakeAPI{}
	srv := httptest.NewServer(api.handler(t))
	defer srv.Close()

	for _, tc := range []struct {
		fast     string
		wantBeta bool
	}{
		{"claude-opus-5-5", true},
		{"claude-haiku-5-5", false},
	} {
		c := New(Config{APIKey: "test-key", FastModel: tc.fast, RefusalFallback: true, BaseURL: srv.URL})
		out, u, err := c.Extract(context.Background(), ExtractRequest{
			System: "s", Prompt: "p", Schema: map[string]any{"type": "object"},
			Attachment: Attachment{MediaType: "image/png", Data: []byte("x")},
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.fast, err)
		}
		if string(out) != `{"ok":true}` || u.Model != tc.fast || u.Total() != 15 {
			t.Fatalf("%s: unexpected %s %+v", tc.fast, out, u)
		}
		api.mu.Lock()
		beta, body := api.betas[len(api.betas)-1], api.bodies[len(api.bodies)-1]
		api.mu.Unlock()
		gotBeta := strings.Contains(beta, FallbackBeta)
		_, gotFallbacks := body["fallbacks"]
		if gotBeta != tc.wantBeta || gotFallbacks != tc.wantBeta {
			t.Errorf("%s: beta header %v (%q), fallbacks param %v; want %v", tc.fast, gotBeta, beta, gotFallbacks, tc.wantBeta)
		}
	}
}
