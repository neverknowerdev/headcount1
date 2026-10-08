package integration

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-orchestrator/db"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

// GatewayHub is the event surface the gateway needs: tenant-scoped delivery
// for run logs (the gateway serves every user's proxy traffic, so events must
// reach only the owning user's clients).
type GatewayHub interface {
	BroadcastEventForCompany(int32, string, interface{})
}

type LLMGateway struct {
	q           *db.Queries
	basePath    string
	hub         GatewayHub
	groupHealth *groupHealthState
	// runCompany caches run → company for tenant-scoped run_log events.
	runCompany sync.Map
	// validateRunToken enables gateway auth (see gateway_auth.go); nil = open.
	validateRunToken func(token string) (int32, bool)
	// validateCompanyToken accepts the tokens of stateless workflow steps.
	validateCompanyToken func(token string) (int32, bool)
}

func NewLLMGateway(database *gorm.DB) *LLMGateway {
	return NewLLMGatewayWithHub(database, nil)
}

func NewLLMGatewayWithHub(database *gorm.DB, hub GatewayHub) *LLMGateway {
	return &LLMGateway{
		q:           db.New(database),
		basePath:    db.Headcount1Home(),
		hub:         hub,
		groupHealth: newGroupHealthState(),
	}
}

func (g *LLMGateway) Mount(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(g.requireGatewayAuth)
		r.Route("/proxy/group/{group_key}", func(r chi.Router) {
			r.Post("/v1/chat/completions", g.proxyChatCompletionsForGroup)
			r.Get("/v1/models", g.getModelsForGroup)
		})
	})
}

// chatCompletionsRequest is the subset of an OpenAI chat-completions body the
// gateway inspects: whether the client wants a stream.
type chatCompletionsRequest struct {
	Stream bool `json:"stream"`
}

func parseChatCompletionsRequest(body []byte) chatCompletionsRequest {
	var req chatCompletionsRequest
	json.Unmarshal(body, &req)
	return req
}

// tokenUsage is what the gateway keeps of a response's usage block: enough
// for a model group's per-member statistics. A caller that accounts for its
// own spend, as an executor session does, reads the response itself.
type tokenUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// proxySSEStream copies an LLM-provider SSE stream to the client line by line
// and returns the usage the provider reported on the way. It also detects
// stalls: if no bytes arrive from the provider for streamStallTimeout, the
// stream is considered dead and an error is returned so the caller fails
// quickly instead of waiting for the outer HTTP timeout.
func proxySSEStream(w http.ResponseWriter, flusher http.Flusher, body io.Reader) (usage tokenUsage, err error) {
	const streamStallTimeout = 30 * time.Second
	// lastByteAt is written by the scanner loop and read by the watchdog
	// goroutine, so it must be atomic (stored as unix nanos).
	var lastByteAt atomic.Int64
	lastByteAt.Store(time.Now().UnixNano())
	done := make(chan struct{})
	stallErr := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if d := time.Since(time.Unix(0, lastByteAt.Load())); d > streamStallTimeout {
					stallErr <- fmt.Errorf("LLM stream stalled: no data for %v", d)
					return
				}
			}
		}
	}()

	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		lastByteAt.Store(time.Now().UnixNano())
		line := scanner.Text()
		fmt.Fprintf(w, "%s\n", line)
		flusher.Flush()

		if strings.HasPrefix(line, "data: ") && line != "data: [DONE]" {
			var chunk struct {
				Usage *tokenUsage `json:"usage"`
			}
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &chunk) == nil && chunk.Usage != nil && chunk.Usage.TotalTokens > 0 {
				usage = *chunk.Usage
			}
		}
	}
	close(done)

	select {
	case stalled := <-stallErr:
		return usage, stalled
	default:
	}
	if err := scanner.Err(); err != nil {
		return usage, fmt.Errorf("stream read error: %w", err)
	}
	return usage, nil
}
