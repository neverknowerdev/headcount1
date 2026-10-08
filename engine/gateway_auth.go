package engine

import (
	"fmt"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/pkg/runtokens"
)

// taskSession names the conversation of a task's smart steps, and runSession
// that of one executor session. A creation time is part of each, so IDs do not
// repeat between installations that count their rows from one.
func taskSession(task db.Task) string {
	return aicli.SessionID("task/", task.ID, "/", task.CreatedAt.UnixNano())
}

func runSession(run db.Run) string {
	return aicli.SessionID("run/", run.ID, "/", run.StartedAt.UnixNano())
}

// modelGroupGatewayHeaders authenticates an executor session to the
// in-process model-group gateway and names its run, so the gateway can add
// the model switches it makes to the session's log.
func modelGroupGatewayHeaders(runID int32, token string) map[string]string {
	return map[string]string{
		runtokens.TokenHeader: token,
		"X-Run-ID":            fmt.Sprintf("%d", runID),
	}
}
