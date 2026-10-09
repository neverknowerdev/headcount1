package aicli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ToolChoiceRequired forces the model to answer with a tool call.
const ToolChoiceRequired = "required"

// maxCorrectiveRetries bounds how often CompleteWithTool re-asks after a
// reply that carried no acceptable tool call.
const maxCorrectiveRetries = 2

// ErrNoToolCall is returned (wrapped) when the model never produced an
// acceptable tool call within the corrective-retry budget. Callers treat it
// as a transient step failure (back off, then escalate), never as a loop.
var ErrNoToolCall = errors.New("model did not return an acceptable tool call")

// OneShotAttempt is one provider round trip made by CompleteWithTool. Every
// attempt is reported — including rejected and failed ones — because each
// costs tokens and time and belongs in the usage ledger.
type OneShotAttempt struct {
	Response *ChatResponse
	Raw      []byte
	Err      error
	Duration time.Duration
}

// OneShotResult is the outcome of a stateless single-tool-call request.
type OneShotResult struct {
	// Call is the accepted tool call.
	Call ToolCall
	// Ignored holds any further tool calls the model sent in the same reply.
	// A stateless step applies exactly one.
	Ignored  []ToolCall
	Attempts []OneShotAttempt
}

// toolChoiceUnsupported remembers provider+model targets that rejected
// tool_choice, so later calls skip straight to the downgraded request.
var toolChoiceUnsupported sync.Map // "baseURL|model" → struct{}

func (c *Client) toolChoiceKey() string { return c.BaseURL + "|" + c.Model }

// CompleteWithTool sends one stateless request and returns exactly one
// validated tool call. req carries the complete prompt; nothing is kept
// between calls.
//
// validate inspects a candidate call (tool name and arguments) and returns a
// non-nil error to reject it. A reply with no tool call, an unknown tool, or
// a rejected call is retried with a corrective line appended to the same
// prompt, at most maxCorrectiveRetries times. A provider that rejects
// tool_choice is retried once without it and remembered. The returned result
// always lists every attempt made, also alongside an error.
func (c *Client) CompleteWithTool(ctx context.Context, req ChatRequest, validate func(ToolCall) error) (*OneShotResult, error) {
	if len(req.Tools) == 0 {
		return nil, errors.New("CompleteWithTool requires at least one tool")
	}
	known := make(map[string]bool, len(req.Tools))
	names := make([]string, 0, len(req.Tools))
	for _, tool := range req.Tools {
		known[tool.Function.Name] = true
		names = append(names, tool.Function.Name)
	}

	result := &OneShotResult{}
	basePrompt := append([]Message(nil), req.Messages...)
	_, downgraded := toolChoiceUnsupported.Load(c.toolChoiceKey())

	var lastReject string
	for corrective := 0; corrective <= maxCorrectiveRetries; corrective++ {
		attempt := req
		attempt.Messages = basePrompt
		if lastReject != "" {
			attempt.Messages = append(append([]Message(nil), basePrompt...), Message{
				Role: "user",
				Content: fmt.Sprintf("Your previous reply was not accepted: %s. Reply with exactly one call to one of these tools: %s.",
					lastReject, strings.Join(names, ", ")),
			})
		}
		attempt.ToolChoice = ToolChoiceRequired
		if downgraded {
			attempt.ToolChoice = ""
		}

		resp, err := c.timedComplete(ctx, attempt, result)
		if err != nil && !downgraded && rejectsToolChoice(ctx, err) {
			// The provider refuses the parameter itself. Retry the same prompt
			// once without it; this does not consume a corrective retry.
			downgraded = true
			toolChoiceUnsupported.Store(c.toolChoiceKey(), struct{}{})
			attempt.ToolChoice = ""
			resp, err = c.timedComplete(ctx, attempt, result)
		}
		if err != nil {
			return result, err
		}

		if len(resp.Choices) == 0 {
			lastReject = "the reply was empty"
			continue
		}
		calls := resp.Choices[0].Message.ToolCalls
		if len(calls) == 0 {
			lastReject = "it contained no tool call"
			continue
		}
		call := calls[0]
		if !known[call.Function.Name] {
			lastReject = fmt.Sprintf("tool %q does not exist", call.Function.Name)
			continue
		}
		if validate != nil {
			if vErr := validate(call); vErr != nil {
				lastReject = fmt.Sprintf("the arguments for %s were invalid (%v)", call.Function.Name, vErr)
				continue
			}
		}
		result.Call = call
		result.Ignored = calls[1:]
		return result, nil
	}
	return result, fmt.Errorf("%w: %s", ErrNoToolCall, lastReject)
}

func (c *Client) timedComplete(ctx context.Context, req ChatRequest, result *OneShotResult) (*ChatResponse, error) {
	started := time.Now()
	resp, raw, err := c.Complete(ctx, req)
	result.Attempts = append(result.Attempts, OneShotAttempt{Response: resp, Raw: raw, Err: err, Duration: time.Since(started)})
	return resp, err
}

// rejectsToolChoice reports whether a provider error is a refusal of the
// tool_choice parameter rather than a failure of the request as a whole.
// Providers word and shape this rejection differently (a typed error body, a
// bare string, a non-JSON 400), so it matches on the parameter name.
func rejectsToolChoice(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "tool_choice")
}
