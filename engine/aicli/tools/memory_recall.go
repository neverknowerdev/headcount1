package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// MemoryRecall searches the company's long-term memory (Hindsight): project
// documentation memories plus the experience of every agent's past runs.
// There is no matching memory_store tool on purpose — retention happens
// automatically (docs on sync, run outcomes on session end).
type MemoryRecall struct {
	fn func(ctx context.Context, query, agent string, maxTokens int) (string, error)
}

func NewMemoryRecall(fn func(ctx context.Context, query, agent string, maxTokens int) (string, error)) *MemoryRecall {
	return &MemoryRecall{fn: fn}
}

func (t *MemoryRecall) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "memory_recall",
			Description: "Search the company's long-term memory for relevant facts and past experience: project " +
				"documentation, previous task outcomes, errors other agents hit, decisions made. Use it before " +
				"starting work, when a task references earlier work, or when you need to know whether something " +
				"was already attempted. Your system prompt already includes a synthesized memory briefing " +
				"(project state, your own playbook) computed in the background — use this tool to dig deeper on " +
				"something specific, not to re-fetch what's already there. Storing happens automatically — there is no memory_store.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"query":{
						"type":"string",
						"description":"Natural-language description of what you want to remember, e.g. \"ICP backend implementation for GM Coin — status, errors, decisions\""
					},
					"agent":{
						"type":"string",
						"description":"Optional: restrict experience memories to one agent role (e.g. \"CTO\"). Omit to search everything."
					}
				},
				"required":["query"]
			}`),
		},
	}
}

func (t *MemoryRecall) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query string `json:"query"`
		Agent string `json:"agent"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("memory_recall: %w", err)
	}
	if p.Query == "" {
		return "", fmt.Errorf("query is required")
	}
	// 0 lets the memory service apply its own default recall token budget.
	out, err := t.fn(ctx, p.Query, p.Agent, 0)
	if err != nil {
		return "", fmt.Errorf("memory_recall: %w", err)
	}
	return out, nil
}
