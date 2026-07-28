package integration

import (
	"context"
	"fmt"
	"log"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/logging"
)

// recordSystemCall persists one non-session LLM exchange — a call that
// belongs to no agent run, like the Hindsight memory engine's retain /
// reflect / consolidation traffic arriving via the path-addressed provider
// proxy. Full request/response bodies go to a JSON file under
// data/logs/system-llm/ (daily folders); the metadata lands as a
// SystemLLMLog row for the Run Logs UI. Best-effort: logging must never
// fail the proxied call itself.
func (g *LLMGateway) recordSystemCall(
	source string,
	provider db.LLMProvider,
	model string,
	reqBody, respBody []byte,
	usage normalizedUsage,
	duration time.Duration,
	callErr error,
) {
	status, errMsg := "ok", ""
	if callErr != nil {
		status, errMsg = "error", callErr.Error()
	}
	filePath, ferr := logging.WriteSystemLLMFile(g.basePath, logging.SystemLLMFile{
		Source:   source,
		Provider: provider.Name,
		Model:    model,
		Request:  reqBody,
		Response: respBody,
		Error:    errMsg,
	})
	if ferr != nil {
		log.Printf("Warning: failed to write system LLM log file: %v", ferr)
	}
	if _, derr := g.q.CreateSystemLLMLog(context.Background(), db.SystemLLMLog{
		Source:           source,
		ProviderID:       provider.ID,
		ProviderName:     provider.Name,
		Model:            model,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
		DurationMs:       duration.Milliseconds(),
		Status:           status,
		Error:            errMsg,
		LogFilePath:      filePath,
	}); derr != nil {
		log.Printf("Warning: failed to store system LLM log: %v", derr)
	}
}

// statusError converts a non-2xx provider status into an error for the
// system LLM log, so failed memory-layer calls are flagged in the UI.
func statusError(statusCode int) error {
	if statusCode >= 400 {
		return fmt.Errorf("provider returned %d", statusCode)
	}
	return nil
}
