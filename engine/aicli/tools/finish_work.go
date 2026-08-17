package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-orchestrator/engine/aicli"
)

type FinishWorkResult struct {
	Status  string `json:"status"`
	Summary string `json:"summary"`
	Details string `json:"details,omitempty"`
}

type FinishWork struct {
	fn func(context.Context, FinishWorkResult) (string, error)
}

func NewFinishWork(fn func(context.Context, FinishWorkResult) (string, error)) *FinishWork {
	return &FinishWork{fn: fn}
}

func (t *FinishWork) Def() aicli.ToolDef {
	return aicli.ToolDef{Type: "function", Function: aicli.FuncMeta{
		Name:        string(aicli.ToolFinishWork),
		Description: "Finish this ephemeral helper-worker assignment with a structured result. Do not create tasks, artifacts, or deliver files.",
		Parameters:  json.RawMessage(`{"type":"object","properties":{"status":{"type":"string","enum":["done","blocked","failed"]},"summary":{"type":"string"},"details":{"type":"string"}},"required":["status","summary"]}`),
	}}
}

func (t *FinishWork) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p FinishWorkResult
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("finish_work: %w", err)
	}
	if p.Status != "done" && p.Status != "blocked" && p.Status != "failed" {
		return "", fmt.Errorf("finish_work: unsupported status %q", p.Status)
	}
	if strings.TrimSpace(p.Summary) == "" {
		return "", fmt.Errorf("finish_work: summary is required")
	}
	return t.fn(ctx, p)
}
