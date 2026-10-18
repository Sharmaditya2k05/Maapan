// Package llm is buddhi's thin seam over the Claude API. Everything else in
// buddhi depends on the Client interface, so tests use a fake and the service
// runs (with AI features answering 503) when no API key is configured.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrDisabled is returned by every call when ANTHROPIC_API_KEY is empty.
var ErrDisabled = errors.New("AI is not configured on this environment (ANTHROPIC_API_KEY is empty)")

// RefusalError is returned when Claude declines the request (stop_reason=refusal).
type RefusalError struct {
	Category    string
	Explanation string
}

func (e *RefusalError) Error() string {
	return fmt.Sprintf("the AI declined this request (%s)", e.Category)
}

// Usage is the token accounting for one feature call (summed over turns).
type Usage struct {
	Model            string `json:"model"`
	InputTokens      int64  `json:"input_tokens"`
	OutputTokens     int64  `json:"output_tokens"`
	CacheReadTokens  int64  `json:"cache_read_tokens"`
	CacheWriteTokens int64  `json:"cache_write_tokens"`
}

// Add accumulates o into u.
func (u *Usage) Add(o Usage) {
	if u.Model == "" {
		u.Model = o.Model
	}
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheReadTokens += o.CacheReadTokens
	u.CacheWriteTokens += o.CacheWriteTokens
}

// Total is what counts against the tenant's monthly budget.
func (u Usage) Total() int64 {
	return u.InputTokens + u.OutputTokens + u.CacheReadTokens + u.CacheWriteTokens
}

// Tier picks the model.
type Tier string

const (
	Smart Tier = "smart" // reasoning-heavy (ask your books, summaries)
	Fast  Tier = "fast"  // high-volume extraction (bill scanning)
)

// Attachment is an image or PDF sent with an extraction request.
type Attachment struct {
	MediaType string // image/jpeg, image/png, image/webp, image/gif, application/pdf
	Data      []byte
}

// ExtractRequest asks for JSON matching Schema (structured outputs).
type ExtractRequest struct {
	System     string
	Prompt     string
	Attachment Attachment
	Schema     map[string]any
	Effort     string // low | medium | high
}

// Tool is a client tool Claude may call during Chat.
type Tool struct {
	Name        string
	Description string
	Properties  map[string]any
	Required    []string
}

// ToolFunc runs a tool. A returned error is sent back to Claude as an error tool_result.
type ToolFunc func(ctx context.Context, name string, input json.RawMessage) (string, error)

// ChatRequest runs a (possibly tool-using) conversation turn to a final answer.
type ChatRequest struct {
	Tier         Tier
	Effort       string
	System       string
	Prompt       string
	Tools        []Tool
	Exec         ToolFunc
	MaxToolCalls int               // hard cap on executed tool calls; 0 = no tools allowed
	MaxTokens    int64             // 0 = default
	OnText       func(text string) // optional streaming callback for text deltas
}

// Client is what the rest of buddhi uses.
type Client interface {
	Enabled() bool
	Extract(ctx context.Context, req ExtractRequest) (json.RawMessage, Usage, error)
	Chat(ctx context.Context, req ChatRequest) (string, Usage, error)
}

// Disabled is used when no API key is configured.
type Disabled struct{}

func (Disabled) Enabled() bool { return false }
func (Disabled) Extract(context.Context, ExtractRequest) (json.RawMessage, Usage, error) {
	return nil, Usage{}, ErrDisabled
}
func (Disabled) Chat(context.Context, ChatRequest) (string, Usage, error) {
	return "", Usage{}, ErrDisabled
}
