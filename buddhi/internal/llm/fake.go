package llm

import (
	"context"
	"encoding/json"
	"sync"
)

// Fake is an in-memory Client for tests: it never touches the network. Extract
// returns ExtractJSON; Chat runs the scripted tool calls through req.Exec
// (exactly like the real agent loop would) and then returns Answer.
type Fake struct {
	ExtractJSON json.RawMessage
	Answer      string
	ToolCalls   []FakeToolCall
	Usage       Usage
	Err         error

	mu          sync.Mutex
	Extracts    []ExtractRequest
	Chats       []ChatRequest
	ToolResults []string // what Exec returned (or "ERROR: ..."), in order
}

// FakeToolCall is one scripted tool call.
type FakeToolCall struct {
	Name  string
	Input string
}

func (f *Fake) Enabled() bool { return true }

func (f *Fake) Extract(_ context.Context, req ExtractRequest) (json.RawMessage, Usage, error) {
	f.mu.Lock()
	f.Extracts = append(f.Extracts, req)
	f.mu.Unlock()
	if f.Err != nil {
		return nil, f.Usage, f.Err
	}
	return f.ExtractJSON, f.Usage, nil
}

func (f *Fake) Chat(ctx context.Context, req ChatRequest) (string, Usage, error) {
	f.mu.Lock()
	f.Chats = append(f.Chats, req)
	f.mu.Unlock()
	if f.Err != nil {
		return "", f.Usage, f.Err
	}
	for i, c := range f.ToolCalls {
		if i >= req.MaxToolCalls || req.Exec == nil {
			break
		}
		out, err := req.Exec(ctx, c.Name, json.RawMessage(c.Input))
		if err != nil {
			out = "ERROR: " + err.Error()
		}
		f.mu.Lock()
		f.ToolResults = append(f.ToolResults, out)
		f.mu.Unlock()
	}
	if req.OnText != nil && f.Answer != "" {
		req.OnText(f.Answer)
	}
	return f.Answer, f.Usage, nil
}
