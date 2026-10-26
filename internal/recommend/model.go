package recommend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// Model answers one request with JSON matching a schema (ADR-0014,
// section 7).
type Model interface {
	Complete(ctx context.Context, system, user string, schema map[string]any) (string, Usage, error)
	Name() string // provider and model, e.g. "anthropic/claude-opus-5-5"
}

// Usage is what a request cost, in tokens.
type Usage struct {
	In  int64 `json:"in"`
	Out int64 `json:"out"`
}

// Add returns the sum of two usages.
func (u Usage) Add(o Usage) Usage { return Usage{In: u.In + o.In, Out: u.Out + o.Out} }

// ErrRefused is returned when the model declines to answer.
var ErrRefused = errors.New("the model declined to answer")

// DefaultAnthropicModel is the model used unless the operator picks another.
const DefaultAnthropicModel = "claude-opus-5-5"

// fallbackModels accept the API's server-side fallback on refusals.
var fallbackModels = map[string]bool{"claude-opus-5-5": true, "claude-opus-5": true, "claude-fable-5-1": true, "claude-sonnet-5-5": true}

// Anthropic drafts with Claude through the official SDK.
type Anthropic struct {
	client anthropic.Client
	model  string
}

// NewAnthropic returns a Claude model; an empty model is the default.
func NewAnthropic(apiKey, model string, opts ...option.RequestOption) *Anthropic {
	if model == "" {
		model = DefaultAnthropicModel
	}
	return &Anthropic{client: anthropic.NewClient(append([]option.RequestOption{option.WithAPIKey(apiKey)}, opts...)...), model: model}
}

// Name names the provider and model.
func (a *Anthropic) Name() string { return "anthropic/" + a.model }

// Complete asks Claude, with the answer's shape enforced by structured
// output.
func (a *Anthropic) Complete(ctx context.Context, system, user string, schema map[string]any) (string, Usage, error) {
	params := anthropic.BetaMessageNewParams{
		Model:     a.model,
		MaxTokens: 16000,
		System:    []anthropic.BetaTextBlockParam{{Text: system}},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(user))},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: anthropic.BetaOutputConfigEffortMedium,
			Format: anthropic.BetaJSONOutputFormatParam{Schema: schema},
		},
	}
	if fallbackModels[a.model] {
		// A refused request is re-served by a fallback model in the same call.
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamOfDefault()
	}
	resp, err := a.client.Beta.Messages.New(ctx, params)
	if err != nil {
		return "", Usage{}, err
	}
	usage := Usage{In: resp.Usage.InputTokens, Out: resp.Usage.OutputTokens}
	if resp.StopReason == anthropic.BetaStopReasonRefusal {
		return "", usage, ErrRefused
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if t, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(t.Text)
		}
	}
	return text.String(), usage, nil
}

// OpenAI drafts with any OpenAI-compatible chat completions endpoint: a
// local model in Ollama, vLLM or LM Studio, or a hosted one.
type OpenAI struct {
	BaseURL string // e.g. http://127.0.0.1:11434/v1
	APIKey  string // empty for local servers
	Model   string
	Client  *http.Client
}

// Name names the provider and model.
func (o *OpenAI) Name() string { return "openai-compatible/" + o.Model }

// maxResponse bounds what's read from a provider.
const maxResponse = 1 << 20

// Complete asks the model, with the answer's shape given as a JSON schema.
func (o *OpenAI) Complete(ctx context.Context, system, user string, schema map[string]any) (string, Usage, error) {
	body, err := json.Marshal(map[string]any{
		"model": o.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"response_format": map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "guardrail", "strict": true, "schema": schema},
		},
	})
	if err != nil {
		return "", Usage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(o.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", Usage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+o.APIKey)
	}
	client := o.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", Usage{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return "", Usage{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return "", Usage{}, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw[:min(len(raw), 300)])))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			Prompt     int64 `json:"prompt_tokens"`
			Completion int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", Usage{}, fmt.Errorf("reading the model's answer: %w", err)
	}
	usage := Usage{In: out.Usage.Prompt, Out: out.Usage.Completion}
	switch {
	case len(out.Choices) == 0:
		return "", usage, errors.New("the model returned no answer")
	case out.Choices[0].Message.Refusal != "":
		return "", usage, ErrRefused
	}
	return out.Choices[0].Message.Content, usage, nil
}
