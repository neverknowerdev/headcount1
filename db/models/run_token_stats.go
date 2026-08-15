package models

type RunTokenStats struct {
	PromptTokens     int            `json:"prompt_tokens"`
	CompletionTokens int            `json:"completion_tokens"`
	ReasoningTokens  int            `json:"reasoning_tokens"`
	ToolInputTokens  int            `json:"tool_input_tokens"`
	ToolOutputTokens int            `json:"tool_output_tokens"`
	CachedTokens     int            `json:"cached_tokens"`
	TotalTokens      int            `json:"total_tokens"`
	MCPToolTokens    int            `json:"mcp_tool_tokens,omitempty"`
	MCPServerTokens  map[string]int `json:"mcp_server_tokens,omitempty"`
}

func (stats RunTokenStats) IsEmpty() bool {
	return stats.PromptTokens == 0 && stats.CompletionTokens == 0 &&
		stats.ReasoningTokens == 0 && stats.ToolInputTokens == 0 &&
		stats.ToolOutputTokens == 0 && stats.CachedTokens == 0 &&
		stats.MCPToolTokens == 0 && len(stats.MCPServerTokens) == 0
}
