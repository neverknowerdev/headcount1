package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// ReportVerificationResults is available ONLY inside QA verification sessions
// (spawned by verify_implementation). The QA agent must call it exactly once
// per verification with a verdict for every acceptance criterion and test
// case item it was given; the engine relays the verdicts back to the session
// that requested the verification.
type ReportVerificationResults struct {
	fn func(ctx context.Context, results []VerificationResult) (string, error)
}

func NewReportVerificationResults(fn func(ctx context.Context, results []VerificationResult) (string, error)) *ReportVerificationResults {
	return &ReportVerificationResults{fn: fn}
}

func (t *ReportVerificationResults) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "report_verification_results",
			Description: "Report the verification verdict for EVERY acceptance criterion and test case item you were " +
				"asked to verify. Base each verdict on actually exercising the implementation (run it, read the files, " +
				"check the outputs) — never on assumption. MUST be called before finishing the verification session.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"results":{
						"type":"array",
						"minItems":1,
						"items":{
							"type":"object",
							"properties":{
								"list":{"type":"string","enum":["acceptance_criteria","test_cases"],"description":"Which item list this verdict belongs to"},
								"id":{"type":"integer","description":"Item id within the list"},
								"success":{"type":"boolean","description":"true if the item passed verification"},
								"error":{"type":"string","description":"Required when success is false: what failed and how to reproduce it"}
							},
							"required":["list","id","success"]
						},
						"description":"One verdict per item — cover every item you were given"
					}
				},
				"required":["results"]
			}`),
		},
	}
}

func (t *ReportVerificationResults) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Results []VerificationResult `json:"results"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("report_verification_results: %w", err)
	}
	if len(p.Results) == 0 {
		return "", fmt.Errorf("results is required")
	}
	for _, r := range p.Results {
		if r.List != "acceptance_criteria" && r.List != "test_cases" {
			return "", fmt.Errorf("invalid list %q: must be acceptance_criteria or test_cases", r.List)
		}
		if !r.Success && r.Error == "" {
			return "", fmt.Errorf("item %d in %s failed but has no error description — explain what failed", r.ID, r.List)
		}
	}
	out, err := t.fn(ctx, p.Results)
	if err != nil {
		return "", fmt.Errorf("report_verification_results: %w", err)
	}
	return out, nil
}
