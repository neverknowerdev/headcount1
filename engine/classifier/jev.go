// Package classifier talks to TypeSafe's Jev, a System One model rather than
// a language model: given a state and a set of typed questions it returns a
// probability, a choice among given options, or a score. It cannot write or
// extract text, so the engine uses it only as a gate: to decide whether
// something is worth asking a language model about.
//
// TypeSafe serves it, and so do other providers beside their language models
// (OpenCode Zen, AI Surplus). All of them take the same request at the
// systemone endpoint next to their chat endpoint.
package classifier

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

const (
	// DefaultModel is the alias of the current Jev model.
	DefaultModel = "jev-latest"
	// ProviderType marks an LLM provider row as a TypeSafe endpoint.
	ProviderType = "typesafe"
	// DefaultBaseURL is TypeSafe's API.
	DefaultBaseURL = "https://api.typesafe.ai"
	// MaxStateChars keeps a state within the model's 32k-token window, with
	// room for the questions.
	MaxStateChars = 90000
	// MaxChoiceOptions is the most options one choice question may offer.
	MaxChoiceOptions = 255
)

// Question is one typed question about the state.
type Question struct {
	Type         string      `json:"type"`
	Instructions string      `json:"instructions"`
	Criteria     interface{} `json:"criteria,omitempty"`
}

// Noul asks a yes/no question; the answer is the probability of yes.
func Noul(instructions string) Question {
	return Question{Type: "noul", Instructions: instructions}
}

// Choice asks which of the labelled options fits; each label maps to its
// description.
func Choice(instructions string, options map[string]string) Question {
	return Question{Type: "choice", Instructions: instructions, Criteria: options}
}

// Answer is the reply to one question. Which fields are set depends on the
// question's type.
type Answer struct {
	// Noul is the probability, from 0 to 1, that the answer is yes.
	Noul float64 `json:"noul"`
	// Choice is the label chosen, with how sure the model is of it.
	Choice     string  `json:"choice"`
	Confidence float64 `json:"confidence"`
}

// Result is what one request cost.
type Result struct {
	Model        string
	InputTokens  int
	OutputTokens int
	Duration     time.Duration
}

// Client calls the systemone endpoint.
type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
	// Backoff is the wait before each retry of a rate-limited or overloaded
	// request; its length is the number of retries.
	Backoff []time.Duration
	// Headers are added to every request: who is calling, which conversation
	// the call belongs to, and what a gateway in between needs.
	Headers map[string]string
}

// New returns a client with the default retry policy.
func New(baseURL, apiKey, model string) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	if strings.TrimSpace(model) == "" {
		model = DefaultModel
	}
	return &Client{
		BaseURL: strings.TrimRight(strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1"), "/"),
		APIKey:  apiKey,
		Model:   model,
		HTTP:    &http.Client{Timeout: 20 * time.Second},
		Backoff: []time.Duration{500 * time.Millisecond, 2 * time.Second},
	}
}

// StatusError is a response the API refused or failed.
type StatusError struct {
	Status int
	Body   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("classifier returned HTTP %d: %s", e.Status, e.Body)
}

func (e *StatusError) retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status == 529
}

// Ask evaluates every question against the state in one request. The
// questions are answered independently of one another. A state too long for
// the model keeps its end, which is where the most recent events are.
func (c *Client) Ask(ctx context.Context, state string, questions map[string]Question) (map[string]Answer, Result, error) {
	if len(questions) == 0 {
		return map[string]Answer{}, Result{}, nil
	}
	if len(state) > MaxStateChars {
		state = "[earlier part omitted]\n" + state[len(state)-MaxStateChars:]
	}
	body, err := json.Marshal(map[string]interface{}{"state": state, "model": c.Model, "questions": questions})
	if err != nil {
		return nil, Result{}, err
	}
	started := time.Now()
	for attempt := 0; ; attempt++ {
		answers, result, err := c.post(ctx, body)
		result.Duration = time.Since(started)
		if err == nil {
			return answers, result, nil
		}
		status, isStatus := err.(*StatusError)
		if !isStatus || !status.retryable() || attempt >= len(c.Backoff) {
			return nil, result, err
		}
		select {
		case <-ctx.Done():
			return nil, result, ctx.Err()
		case <-time.After(c.Backoff[attempt]):
		}
	}
}

func (c *Client) post(ctx context.Context, body []byte) (map[string]Answer, Result, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/v1/systemone", bytes.NewReader(body))
	if err != nil {
		return nil, Result{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	for name, value := range c.Headers {
		request.Header.Set(name, value)
	}
	response, err := c.HTTP.Do(request)
	if err != nil {
		return nil, Result{}, err
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode < 200 || response.StatusCode > 299 {
		text := strings.TrimSpace(string(raw))
		if len(text) > 500 {
			text = text[:500]
		}
		return nil, Result{}, &StatusError{Status: response.StatusCode, Body: text}
	}
	var parsed struct {
		Model   string            `json:"model"`
		Answers map[string]Answer `json:"answers"`
		Usage   struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, Result{}, fmt.Errorf("classifier returned an unreadable answer: %w", err)
	}
	if parsed.Answers == nil {
		parsed.Answers = map[string]Answer{}
	}
	return parsed.Answers, Result{Model: parsed.Model, InputTokens: parsed.Usage.InputTokens, OutputTokens: parsed.Usage.OutputTokens}, nil
}
