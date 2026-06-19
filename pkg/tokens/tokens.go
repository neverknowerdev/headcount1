package tokens

// Estimate returns a rough character/4 token estimate for the given string.
// It intentionally matches the heuristic already used elsewhere in the
// codebase (see engine/opencode.go:263 and pkg/logging/proxy_logger.go:204)
// so the UI's "~N tok" labels are internally consistent.
func Estimate(s string) int {
	if len(s) == 0 {
		return 0
	}
	return len(s) / 4
}

// EstimateBytes is the []byte convenience wrapper.
func EstimateBytes(b []byte) int {
	return Estimate(string(b))
}

// Usage is the shared LLM token usage shape used by both the LLM gateway
// (pkg/logging and integration) and the engine. It mirrors the OpenAI
// usage block and adds the fields the UI cares about (reasoning tokens
// and tool input tokens). The JSON tags double as the on-the-wire shape
// persisted into proxy log entries.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
	ToolInputTokens  int `json:"tool_input_tokens"`
	CachedTokens     int `json:"cached_tokens"` // subset of PromptTokens, provider-reported
}
