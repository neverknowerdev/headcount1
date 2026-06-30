package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// ---------------------------------------------------------------------------
// AskQuestion — used by the SmartPlanner/orchestrator
// ---------------------------------------------------------------------------

// AskQuestionTool allows the orchestrator to ask a single question to the
// specialist researcher agent. Questions from one LLM turn are batched by
// AskQuestionBatcher in the agent loop and processed in parallel.
type AskQuestionTool struct{}

func NewAskQuestion() *AskQuestionTool { return &AskQuestionTool{} }

func (t *AskQuestionTool) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "ask_question",
			Description: "Ask a research question to the specialist researcher agent. " +
				"Questions from the same response are automatically batched and processed in parallel. " +
				"Ask as many questions as you need in a single response to maximise efficiency.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"question": {
						"type": "string",
						"description": "The research question to ask"
					}
				},
				"required": ["question"]
			}`),
		},
	}
}

// Execute should not be called directly. The agent loop intercepts
// ask_question calls and routes them through AskQuestionBatcher.RunBatch.
func (t *AskQuestionTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", fmt.Errorf("ask_question must be executed via AskQuestionBatcher")
}

// ---------------------------------------------------------------------------
// FinishRefinement — used by the SmartPlanner
// ---------------------------------------------------------------------------

// FinishRefinementArgs holds the structured output produced by the planner at
// the end of a refinement session.
type FinishRefinementArgs struct {
	DetailedDescription string `json:"detailed_description"`
	Specifications      string `json:"specifications"`
	AcceptanceCriteria  string `json:"acceptance_criteria"`
	TestCases           string `json:"test_cases"`
}

// FinishRefinementTool signals that the SmartPlanner has finished gathering
// information and is ready to hand its output to the implementation phase.
type FinishRefinementTool struct {
	onFinish func(ctx context.Context, args FinishRefinementArgs) error
}

func NewFinishRefinement(onFinish func(ctx context.Context, args FinishRefinementArgs) error) *FinishRefinementTool {
	return &FinishRefinementTool{onFinish: onFinish}
}

func (t *FinishRefinementTool) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "finish_refinement",
			Description: "Complete the refinement phase by producing the full specification. " +
				"Call this when you have gathered enough information to describe the work in detail.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"detailed_description": {
						"type": "string",
						"description": "A thorough description of what needs to be built or changed"
					},
					"specifications": {
						"type": "string",
						"description": "Technical specifications and requirements"
					},
					"acceptance_criteria": {
						"type": "string",
						"description": "Conditions that must be met for the work to be considered complete"
					},
					"test_cases": {
						"type": "string",
						"description": "Test cases that verify the implementation meets the acceptance criteria"
					}
				},
				"required": ["detailed_description", "specifications", "acceptance_criteria", "test_cases"]
			}`),
		},
	}
}

func (t *FinishRefinementTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p FinishRefinementArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if err := t.onFinish(ctx, p); err != nil {
		return "", fmt.Errorf("finish_refinement: %w", err)
	}
	return "Refinement complete. Specifications saved. Implementation will begin.", nil
}

// ---------------------------------------------------------------------------
// AskTaskOwner — used by Coder/Tester sub-agents to escalate to orchestrator
// ---------------------------------------------------------------------------

// Escalation carries a question from a sub-agent to the SmartPlanner
// orchestrator, along with a channel for the orchestrator's answer.
type Escalation struct {
	SessionID int32
	Question  string
	AnswerCh  chan<- string
}

// AskTaskOwnerTool allows a sub-agent to escalate an unexpected decision to
// the SmartPlanner orchestrator. The call blocks until an answer is received.
type AskTaskOwnerTool struct {
	sessionID    int32
	escalationCh chan<- Escalation
	answerCh     <-chan string
}

func NewAskTaskOwner(sessionID int32, escalationCh chan<- Escalation, answerCh <-chan string) *AskTaskOwnerTool {
	return &AskTaskOwnerTool{
		sessionID:    sessionID,
		escalationCh: escalationCh,
		answerCh:     answerCh,
	}
}

func (t *AskTaskOwnerTool) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "ask_task_owner",
			Description: "Escalate an unexpected decision or ambiguity to the orchestrator. " +
				"Use this when you encounter a situation that requires input from the planner " +
				"before you can proceed. The call will block until an answer is provided.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"question": {
						"type": "string",
						"description": "The question to escalate to the orchestrator"
					}
				},
				"required": ["question"]
			}`),
		},
	}
}

func (t *AskTaskOwnerTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Question string `json:"question"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	answerCh := make(chan string, 1)
	select {
	case t.escalationCh <- Escalation{SessionID: t.sessionID, Question: p.Question, AnswerCh: answerCh}:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	select {
	case answer := <-answerCh:
		return answer, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// ---------------------------------------------------------------------------
// SubAgentAnswer — used by sub-agents to submit final answers and complete
// ---------------------------------------------------------------------------

// QuestionAnswer pairs a question number with the sub-agent's answer.
type QuestionAnswer struct {
	N      int    `json:"n"`
	Answer string `json:"answer"`
}

// SubAgentAnswerTool allows a sub-agent to provide final answers to the
// questions it was assigned and signal that the session is complete.
type SubAgentAnswerTool struct {
	completeCh chan<- []QuestionAnswer
}

func NewSubAgentAnswer(completeCh chan<- []QuestionAnswer) *SubAgentAnswerTool {
	return &SubAgentAnswerTool{completeCh: completeCh}
}

func (t *SubAgentAnswerTool) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "answer_question",
			Description: "Provide answers to the research questions and complete this session.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"answers": {
						"type": "array",
						"description": "Array of answers, one per question",
						"items": {
							"type": "object",
							"properties": {
								"n": {
									"type": "integer",
									"description": "Question number (1-based index matching the question prompt)"
								},
								"answer": {
									"type": "string",
									"description": "The answer to question n"
								}
							},
							"required": ["n", "answer"]
						}
					}
				},
				"required": ["answers"]
			}`),
		},
	}
}

func (t *SubAgentAnswerTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Answers []QuestionAnswer `json:"answers"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	select {
	case t.completeCh <- p.Answers:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return "Answers submitted.", nil
}

// ---------------------------------------------------------------------------
// OrchestratorAnswer — used by SmartPlanner to answer a sub-agent escalation
// ---------------------------------------------------------------------------

// OrchestratorAnswerTool is dynamically registered on the SmartPlanner's
// registry when a pending escalation from a sub-agent is waiting for an answer.
// It should be deregistered once the answer has been delivered.
type OrchestratorAnswerTool struct {
	sessionID int32
	answerCh  chan<- string
}

func NewOrchestratorAnswer(sessionID int32, answerCh chan<- string) *OrchestratorAnswerTool {
	return &OrchestratorAnswerTool{sessionID: sessionID, answerCh: answerCh}
}

func (t *OrchestratorAnswerTool) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "answer_question",
			Description: "Answer a question asked by a sub-agent (coder or tester). " +
				"This tool is only available when a sub-agent is waiting for a response.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"answer": {
						"type": "string",
						"description": "The answer to deliver to the waiting sub-agent"
					}
				},
				"required": ["answer"]
			}`),
		},
	}
}

func (t *OrchestratorAnswerTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Answer string `json:"answer"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	select {
	case t.answerCh <- p.Answer:
	case <-ctx.Done():
		return "", ctx.Err()
	}
	return "Answer delivered to sub-agent.", nil
}
