package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// ReportStatus lets an agent publish a short progress line for its current
// run. The engine stores it on the run record and broadcasts it, so the
// orchestrator and the UI can see what each session is doing right now.
type ReportStatus struct {
	fn func(ctx context.Context, status string) error
}

func NewReportStatus(fn func(ctx context.Context, status string) error) *ReportStatus {
	return &ReportStatus{fn: fn}
}

func (t *ReportStatus) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: string(aicli.ToolReportStatus),
			Description: "Report a short one-line progress status for the current run (e.g. \"Refining requirements\", " +
				"\"Implementing API endpoint\"). Call it whenever you start a new stage of work so progress is visible.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"status":{
						"type":"string",
						"description":"One short line describing what you are doing right now"
					}
				},
				"required":["status"]
			}`),
		},
	}
}

func (t *ReportStatus) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("report_status: %w", err)
	}
	if p.Status == "" {
		return "", fmt.Errorf("status is required")
	}
	if err := t.fn(ctx, p.Status); err != nil {
		return "", fmt.Errorf("report_status: %w", err)
	}
	return "Status recorded.", nil
}
