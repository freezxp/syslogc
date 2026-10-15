// Package llm talks to a model served by Ollama.
//
// It exists to keep one fact in one place: a language model is never the
// source of a number here. It is asked either to turn a question into a
// query, which Syslogc then runs and whose results come from the store, or
// to put computed figures into words. Nothing it returns is acted on without
// a person seeing it first.
//
// The model runs wherever the deployment says — on the same host, or on a
// machine with a GPU. Log content never leaves that network, which is the
// point of running one locally rather than calling a hosted service.
package llm

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

// Client is a model endpoint.
type Client struct {
	url   string
	model string
	http  *http.Client
}

// Options configure a client.
type Options struct {
	// URL is the Ollama base address, e.g. http://127.0.0.1:11434.
	URL string
	// Model is the name as Ollama lists it, e.g. qwen3.5:9b.
	Model string
	// Timeout bounds one request. A small model on a busy GPU takes seconds;
	// a large one on a CPU takes minutes, and waiting that long behind an
	// HTTP handler helps nobody.
	Timeout time.Duration
}

// New returns a client, or an error describing what is missing.
func New(o Options) (*Client, error) {
	url := strings.TrimRight(strings.TrimSpace(o.URL), "/")
	if url == "" {
		return nil, fmt.Errorf("the assistant needs a model endpoint; set assistant.url")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil, fmt.Errorf("assistant.url must start with http:// or https://; got %q", o.URL)
	}
	if strings.TrimSpace(o.Model) == "" {
		return nil, fmt.Errorf("the assistant needs a model name; set assistant.model")
	}
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &Client{url: url, model: o.Model, http: &http.Client{Timeout: timeout}}, nil
}

// Model is the model this client asks.
func (c *Client) Model() string { return c.model }

// Endpoint is where it asks, for status and error messages.
func (c *Client) Endpoint() string { return c.url }

type generateRequest struct {
	Model  string          `json:"model"`
	Prompt string          `json:"prompt"`
	System string          `json:"system,omitempty"`
	Stream bool            `json:"stream"`
	Format json.RawMessage `json:"format,omitempty"`
	// Think is sent explicitly, including when false: a reasoning model left
	// to itself spends its whole token budget thinking and returns nothing
	// at all. Measured on qwen3.5:9b asking for one filter — 6.3s and an
	// empty answer with reasoning on, 0.4s and the right answer with it off.
	Think   *bool          `json:"think,omitempty"`
	Options map[string]any `json:"options,omitempty"`
}

type generateResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
	// Counts, for reporting what a request cost.
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
	Error           string `json:"error"`
}

// Result is one completion and what it cost.
type Result struct {
	Text         string
	PromptTokens int
	Tokens       int
	Took         time.Duration
}

// Ask sends a prompt and returns the model's text.
//
// schema, when given, is a JSON Schema the reply must satisfy: Ollama
// constrains the model's output to match it, which turns "usually valid
// JSON" into "valid JSON".
func (c *Client) Ask(ctx context.Context, system, prompt string, schema any) (Result, error) {
	req := generateRequest{
		Model:  c.model,
		Prompt: prompt,
		System: system,
		Stream: false,
		Options: map[string]any{
			// Deterministic: the same question should give the same query,
			// or nobody can report a problem with it.
			"temperature": 0,
			"num_predict": 800,
		},
	}
	// Reasoning is off for everything, not only for structured replies.
	// Measured on qwen3.5:9b: with it on, a request for one filter spent 6.3s
	// and 600 tokens and returned an empty string, and a request for prose
	// spent its whole 800-token budget thinking and returned nothing. With it
	// off the same filter came back correct in 0.4s. A model that thinks
	// itself out of answering is worse than one that answers plainly.
	off := false
	req.Think = &off
	if schema != nil {
		raw, err := json.Marshal(schema)
		if err != nil {
			return Result{}, fmt.Errorf("encoding the reply schema: %w", err)
		}
		req.Format = raw
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Result{}, fmt.Errorf("encoding the request: %w", err)
	}

	started := time.Now()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return Result{}, fmt.Errorf("the model at %s did not answer: %w", c.url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return Result{}, fmt.Errorf("reading the model's reply: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return Result{}, modelError(resp.StatusCode, payload, c.model, c.url)
	}
	var out generateResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return Result{}, fmt.Errorf("the model's reply was not JSON: %w", err)
	}
	if out.Error != "" {
		return Result{}, fmt.Errorf("the model reported: %s", out.Error)
	}
	text := strings.TrimSpace(out.Response)
	if text == "" {
		// Almost always a reasoning model that spent its budget thinking.
		return Result{}, fmt.Errorf("the model returned nothing after %d tokens; "+
			"it may be a reasoning model whose output budget went on thinking", out.EvalCount)
	}
	return Result{
		Text: text, PromptTokens: out.PromptEvalCount, Tokens: out.EvalCount,
		Took: time.Since(started),
	}, nil
}

// modelError turns Ollama's reply into something that says what to do.
func modelError(status int, body []byte, model, url string) error {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	msg := strings.TrimSpace(e.Error)
	switch {
	case status == http.StatusNotFound && strings.Contains(strings.ToLower(msg), "not found"):
		return fmt.Errorf("%s has no model called %q; pull it there with: ollama pull %s", url, model, model)
	case msg != "":
		return fmt.Errorf("the model endpoint refused the request (%d): %s", status, msg)
	default:
		return fmt.Errorf("the model endpoint refused the request (%d)", status)
	}
}

// Models lists what the endpoint can serve, for checking configuration.
func (c *Client) Models(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the model endpoint at %s did not answer: %w", c.url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return nil, modelError(resp.StatusCode, body, c.model, c.url)
	}
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return nil, fmt.Errorf("the model list was not JSON: %w", err)
	}
	names := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		names = append(names, m.Name)
	}
	return names, nil
}

// JSON parses a schema-constrained reply into v.
func (r Result) JSON(v any) error {
	if err := json.Unmarshal([]byte(r.Text), v); err != nil {
		return fmt.Errorf("the model's reply was not the JSON that was asked for: %w", err)
	}
	return nil
}
