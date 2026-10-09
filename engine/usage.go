package engine

import (
	"context"
	"fmt"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/db/models"
	"agent-orchestrator/engine/aicli"
)

// callContext says on whose behalf a model call is made. Every call of any
// kind is recorded in the usage ledger with it.
type callContext struct {
	CompanyID     int32
	RootTaskID    int32
	TaskID        int32
	AgentID       *int32
	AgentName     string
	Purpose       string
	Phase         string
	WorkflowPhase string
	StepID        *int64
	RunID         *int32
	LogSeq        *int64
}

// callContextFor describes calls made by a task in a phase.
func callContextFor(task db.Task, phase, purpose string, agent *db.Agent) callContext {
	workflowPhase := task.WorkflowPhase
	if task.ParentID == nil || workflowPhase == "" {
		// A top-level task's work belongs to the phase it is in.
		workflowPhase = phase
	}
	c := callContext{
		CompanyID: task.CompanyID, RootTaskID: task.RootTaskID, TaskID: task.ID,
		Purpose: purpose, Phase: phase, WorkflowPhase: workflowPhase,
	}
	if agent != nil {
		id := agent.ID
		c.AgentID = &id
		c.AgentName = agent.Name
	}
	return c
}

// recordCall writes one model call to the usage ledger. The ledger is
// bookkeeping: a failure to write it is logged and never fails the call it
// describes.
func recordCall(ctx context.Context, q *db.Queries, c callContext, target modelTarget, answeredBy string, usage aicli.Usage, duration time.Duration, status, errText string) {
	call := db.LLMCall{
		CompanyID: c.CompanyID, AgentID: c.AgentID, AgentName: c.AgentName,
		Tier: target.Tier, Purpose: c.Purpose, Phase: c.Phase, WorkflowPhase: c.WorkflowPhase,
		ProviderName: target.Provider.Name, Model: answeredBy, RequestedModel: target.Model,
		StepID: c.StepID, RunID: c.RunID, LogSeq: c.LogSeq,
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		ReasoningTokens:  usage.CompletionTokensDetails.ReasoningTokens,
		CachedTokens:     usage.PromptTokensDetails.CachedTokens,
		DurationMs:       duration.Milliseconds(),
		Status:           status, Error: errText, CreatedAt: time.Now(),
	}
	if c.RootTaskID != 0 {
		call.RootTaskID = &c.RootTaskID
	}
	if c.TaskID != 0 {
		call.TaskID = &c.TaskID
	}
	if target.Provider.ID != 0 {
		id := target.Provider.ID
		call.ProviderID = &id
	}
	if call.Model == "" {
		call.Model = target.Model
	}
	if _, err := q.RecordLLMCall(ctx, call); err != nil {
		fmt.Printf("Warning: failed to record model call for task %d: %v\n", c.TaskID, err)
	}
}

// recordAttempts writes every provider round trip of a one-shot call. The
// last attempt of a successful call is the one that was accepted; the ones
// before it were answered but rejected, or failed outright.
func recordAttempts(ctx context.Context, q *db.Queries, c callContext, target modelTarget, result *aicli.OneShotResult, accepted bool) {
	if result == nil {
		return
	}
	for i, attempt := range result.Attempts {
		status, errText := models.LLMCallRejected, ""
		switch {
		case attempt.Err != nil:
			status, errText = models.LLMCallError, attempt.Err.Error()
		case accepted && i == len(result.Attempts)-1:
			status = models.LLMCallOK
		}
		var usage aicli.Usage
		answeredBy := ""
		if attempt.Response != nil {
			usage = attempt.Response.Usage
			answeredBy = attempt.Response.Model
		}
		recordCall(ctx, q, c, target, answeredBy, usage, attempt.Duration, status, errText)
	}
}
