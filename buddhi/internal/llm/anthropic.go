package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Config selects models and behaviour. Defaults: claude-opus-5-5 for both tiers;
// set AI_MODEL_FAST=claude-haiku-5-5 to cut bill-scanning cost if quality holds.
type Config struct {
	APIKey          string
	SmartModel      string
	FastModel       string
	RefusalFallback bool // server-side fallback ("default" routing) when a request is declined
	Timeout         time.Duration
}

// Anthropic implements Client with the official Go SDK.
type Anthropic struct {
	c   anthropic.Client
	cfg Config
}

// New returns a Disabled client when APIKey is empty.
func New(cfg Config) Client {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return Disabled{}
	}
	if cfg.SmartModel == "" {
		cfg.SmartModel = "claude-opus-5-5"
	}
	if cfg.FastModel == "" {
		cfg.FastModel = "claude-opus-5-5"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 3 * time.Minute
	}
	return &Anthropic{
		c: anthropic.NewClient(
			option.WithAPIKey(cfg.APIKey),
			option.WithMaxRetries(2),
			option.WithRequestTimeout(cfg.Timeout),
		),
		cfg: cfg,
	}
}

func (a *Anthropic) Enabled() bool { return true }

func (a *Anthropic) model(t Tier) string {
	if t == Fast {
		return a.cfg.FastModel
	}
	return a.cfg.SmartModel
}

// opts adds the server-side refusal fallback. A declined request is re-served by
// a fallback model inside the same call instead of simply stopping.
func (a *Anthropic) opts() []option.RequestOption {
	if !a.cfg.RefusalFallback {
		return nil
	}
	return []option.RequestOption{
		option.WithHeaderAdd("anthropic-beta", "server-side-fallback-2026-07-01"),
		option.WithJSONSet("fallbacks", "default"),
	}
}

func effort(e string) anthropic.OutputConfigEffort {
	switch e {
	case "low":
		return anthropic.OutputConfigEffortLow
	case "high":
		return anthropic.OutputConfigEffortHigh
	default:
		return anthropic.OutputConfigEffortMedium
	}
}

func usageOf(model string, u anthropic.Usage) Usage {
	return Usage{
		Model: model, InputTokens: u.InputTokens, OutputTokens: u.OutputTokens,
		CacheReadTokens: u.CacheReadInputTokens, CacheWriteTokens: u.CacheCreationInputTokens,
	}
}

func checkStop(m *anthropic.Message) error {
	switch m.StopReason {
	case anthropic.StopReasonRefusal:
		return &RefusalError{Category: string(m.StopDetails.Category), Explanation: m.StopDetails.Explanation}
	case anthropic.StopReasonMaxTokens:
		return errors.New("AI response was cut off (max_tokens)")
	}
	return nil
}

// Extract sends one image/PDF and returns JSON constrained to req.Schema.
func (a *Anthropic) Extract(ctx context.Context, req ExtractRequest) (json.RawMessage, Usage, error) {
	model := a.model(Fast)
	b64 := base64.StdEncoding.EncodeToString(req.Attachment.Data)
	var file anthropic.ContentBlockParamUnion
	if req.Attachment.MediaType == "application/pdf" {
		file = anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{Data: b64})
	} else {
		file = anthropic.NewImageBlockBase64(req.Attachment.MediaType, b64)
	}
	resp, err := a.c.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     model,
		MaxTokens: 16000,
		System:    []anthropic.TextBlockParam{{Text: req.System, CacheControl: anthropic.NewCacheControlEphemeralParam()}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(file, anthropic.NewTextBlock(req.Prompt))},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: effort(req.Effort),
			Format: anthropic.JSONOutputFormatParam{Schema: req.Schema},
		},
	}, a.opts()...)
	if err != nil {
		return nil, Usage{}, wrapAPIError(err)
	}
	u := usageOf(model, resp.Usage)
	if err := checkStop(resp); err != nil {
		return nil, u, err
	}
	var sb strings.Builder
	for _, b := range resp.Content {
		if t, ok := b.AsAny().(anthropic.TextBlock); ok {
			sb.WriteString(t.Text)
		}
	}
	out := json.RawMessage(strings.TrimSpace(sb.String()))
	if !json.Valid(out) {
		return nil, u, errors.New("AI returned invalid JSON")
	}
	return out, u, nil
}

// Chat streams every turn (long answers never hit HTTP timeouts), runs client
// tools up to MaxToolCalls, then forces a final text answer.
func (a *Anthropic) Chat(ctx context.Context, req ChatRequest) (string, Usage, error) {
	model := a.model(req.Tier)
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 16000
	}
	var tools []anthropic.ToolUnionParam
	for _, t := range req.Tools {
		tp := anthropic.ToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties:  t.Properties,
				Required:    t.Required,
				ExtraFields: map[string]any{"additionalProperties": false},
			},
			// Streaming + client tools: stream tool input as generated. We validate
			// every input ourselves in Exec (see ask.Tools), since the API doesn't.
			EagerInputStreaming: anthropic.Bool(true),
		}
		tools = append(tools, anthropic.ToolUnionParam{OfTool: &tp})
	}
	params := anthropic.MessageNewParams{
		Model:        model,
		MaxTokens:    maxTokens,
		System:       []anthropic.TextBlockParam{{Text: req.System, CacheControl: anthropic.NewCacheControlEphemeralParam()}},
		Messages:     []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(req.Prompt))},
		Tools:        tools,
		OutputConfig: anthropic.OutputConfigParam{Effort: effort(req.Effort)},
	}

	var total Usage
	calls := 0
	for turn := 0; turn < req.MaxToolCalls+2; turn++ {
		if len(tools) > 0 && calls >= req.MaxToolCalls {
			params.ToolChoice = anthropic.ToolChoiceUnionParam{OfNone: &anthropic.ToolChoiceNoneParam{}}
		}
		msg, err := a.stream(ctx, params, req.OnText)
		total.Add(usageOf(model, msg.Usage))
		if err != nil {
			return "", total, err
		}
		if err := checkStop(&msg); err != nil {
			return "", total, err
		}
		params.Messages = append(params.Messages, msg.ToParam())
		if msg.StopReason != anthropic.StopReasonToolUse {
			return finalText(&msg), total, nil
		}

		var results []anthropic.ContentBlockParamUnion
		for _, b := range msg.Content {
			tu, ok := b.AsAny().(anthropic.ToolUseBlock)
			if !ok {
				continue
			}
			if calls >= req.MaxToolCalls || req.Exec == nil {
				results = append(results, anthropic.NewToolResultBlock(tu.ID, "Tool budget exhausted. Answer now with the data you already have.", true))
				continue
			}
			calls++
			out, err := req.Exec(ctx, tu.Name, json.RawMessage(tu.JSON.Input.Raw()))
			if err != nil {
				results = append(results, anthropic.NewToolResultBlock(tu.ID, "ERROR: "+err.Error(), true))
				continue
			}
			results = append(results, anthropic.NewToolResultBlock(tu.ID, out, false))
		}
		// All tool results go back in ONE user message.
		params.Messages = append(params.Messages, anthropic.NewUserMessage(results...))
	}
	return "", total, errors.New("AI did not finish within the tool-call limit")
}

func (a *Anthropic) stream(ctx context.Context, params anthropic.MessageNewParams, onText func(string)) (anthropic.Message, error) {
	s := a.c.Messages.NewStreaming(ctx, params, a.opts()...)
	defer s.Close()
	msg := anthropic.Message{}
	for s.Next() {
		ev := s.Current()
		if err := msg.Accumulate(ev); err != nil {
			return msg, err
		}
		if onText == nil {
			continue
		}
		if d, ok := ev.AsAny().(anthropic.ContentBlockDeltaEvent); ok {
			if t, ok := d.Delta.AsAny().(anthropic.TextDelta); ok {
				onText(t.Text)
			}
		}
	}
	if err := s.Err(); err != nil {
		return msg, wrapAPIError(err)
	}
	return msg, nil
}

func finalText(m *anthropic.Message) string {
	var sb strings.Builder
	for _, b := range m.Content {
		if t, ok := b.AsAny().(anthropic.TextBlock); ok {
			sb.WriteString(t.Text)
		}
	}
	return strings.TrimSpace(sb.String())
}

// UpstreamError marks failures from the Claude API (rate limits, outages).
type UpstreamError struct {
	Status    int
	Retryable bool
	Err       error
}

func (e *UpstreamError) Error() string { return fmt.Sprintf("Claude API error (status %d)", e.Status) }
func (e *UpstreamError) Unwrap() error { return e.Err }

func wrapAPIError(err error) error {
	var apierr *anthropic.Error
	if errors.As(err, &apierr) {
		s := apierr.StatusCode
		return &UpstreamError{Status: s, Retryable: s == 429 || s >= 500, Err: err}
	}
	return err
}
