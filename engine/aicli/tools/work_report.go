package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-orchestrator/engine/aicli"
)

// Outcomes an executor can report.
const (
	WorkDone           = "done"
	WorkFailed         = "failed"
	WorkCannotComplete = "cannot_complete"
)

// Verdicts a review reports.
const (
	VerdictApproved         = "approved"
	VerdictChangesRequested = "changes_requested"
)

// WorkReport is what an executor says when it finishes: the outcome, what it
// found or did, the proof, and whatever it decided on the way that it has not
// recorded yet.
type WorkReport struct {
	Status        string        `json:"status"`
	Summary       string        `json:"summary"`
	Details       string        `json:"details,omitempty"`
	Evidence      []string      `json:"evidence,omitempty"`
	Decisions     []RecordInput `json:"decisions,omitempty"`
	DeadEnds      []RecordInput `json:"dead_ends,omitempty"`
	OpenQuestions []string      `json:"open_questions,omitempty"`
	Verdict       string        `json:"verdict,omitempty"`
}

// WorkReportTool is the executor's terminal tool, finish_work.
type WorkReportTool struct {
	// review makes the verdict mandatory: a review that does not say whether
	// the work is acceptable has not done its job.
	review bool
	fn     func(context.Context, WorkReport) (string, error)
}

func NewWorkReport(review bool, fn func(context.Context, WorkReport) (string, error)) *WorkReportTool {
	return &WorkReportTool{review: review, fn: fn}
}

func (t *WorkReportTool) Def() aicli.ToolDef {
	properties := `"status":{"type":"string","enum":["done","failed","cannot_complete"],"description":"done: the task is finished as instructed. failed: you attempted it and it did not work. cannot_complete: it cannot be done or answered with what is available; say why."},
"summary":{"type":"string","description":"The result in a few sentences: the answer, what was changed, or what stopped you. This is what the task's owner reads first."},
"details":{"type":"string","description":"Everything the owner may need beyond the summary: specifics, what you tried, exact errors."},
"evidence":{"type":"array","items":{"type":"string"},"description":"What shows the result is true: commands run and their outcome, files and lines, sources."},
"decisions":` + recordListSchema("Choices you made that you have not recorded in a checkpoint.", "What you decided.", "Why.") + `,
"dead_ends":` + recordListSchema("Approaches you tried and abandoned that you have not recorded in a checkpoint.", "What you tried.", "Why it did not work.") + `,
"open_questions":{"type":"array","items":{"type":"string"},"description":"Questions you could not settle and the owner should know about."}`
	required := `["status","summary"]`
	description := "Finish the task and report the result. Report honestly: a clear failure with the reason is more useful than a doubtful success."
	if t.review {
		properties += `,
"verdict":{"type":"string","enum":["approved","changes_requested"],"description":"approved: the work meets its instructions. changes_requested: it does not; the summary lists exactly what must change."}`
		required = `["status","summary","verdict"]`
		description += " As a reviewer, give a verdict."
	}
	return aicli.ToolDef{Type: "function", Function: aicli.FuncMeta{
		Name:        string(aicli.ToolFinishWork),
		Description: description,
		Parameters:  json.RawMessage(`{"type":"object","properties":{` + properties + `},"required":` + required + `}`),
	}}
}

func (t *WorkReportTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var report WorkReport
	if err := json.Unmarshal(args, &report); err != nil {
		return "", fmt.Errorf("finish_work: %w", err)
	}
	switch report.Status {
	case WorkDone, WorkFailed, WorkCannotComplete:
	default:
		return "", fmt.Errorf("finish_work: status must be done, failed or cannot_complete")
	}
	if strings.TrimSpace(report.Summary) == "" {
		return "", fmt.Errorf("finish_work: summary is required")
	}
	if t.review && report.Status == WorkDone {
		if report.Verdict != VerdictApproved && report.Verdict != VerdictChangesRequested {
			return "", fmt.Errorf("finish_work: a finished review needs a verdict of approved or changes_requested")
		}
	}
	if !t.review {
		report.Verdict = ""
	}
	for field, records := range map[string][]RecordInput{"decisions": report.Decisions, "dead_ends": report.DeadEnds} {
		if err := validateRecords(field, records); err != nil {
			return "", fmt.Errorf("finish_work: %w", err)
		}
	}
	return t.fn(ctx, report)
}
