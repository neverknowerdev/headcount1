package engine

import (
	"context"
	"fmt"
	"strconv"

	"agent-orchestrator/db"
)

// assignRunName gives a fresh run its stable human-readable key. Root runs
// use the task reference, agent short name, and main-session ordinal. Delegated
// sessions append their ordinal for that agent within the same main session.
func assignRunName(ctx context.Context, q *db.Queries, task db.Task, agent db.Agent, run db.Run, parent *parentSession, rootTaskID, rootRunID int32) db.Run {
	shortName := agent.ShortName
	if shortName == "" {
		shortName = agent.Name
	}
	taskRef := task.RefKey
	if rootTaskID != task.ID {
		if rootTask, rootErr := q.GetTask(ctx, rootTaskID); rootErr == nil && rootTask.RefKey != "" {
			taskRef = rootTask.RefKey
		}
	}
	if taskRef == "" {
		taskRef = fmt.Sprintf("TASK-%d", rootTaskID)
	}

	mainRunNumber := int64(1)
	if prior, err := q.CountRootRunsThrough(ctx, rootTaskID, rootRunID); err == nil && prior > 0 {
		mainRunNumber = prior
	}
	runKey := fmt.Sprintf("%s-%s-%s", taskRef, shortName, strconv.FormatInt(mainRunNumber, 10))
	if parent != nil {
		subRunNumber := int64(1)
		if prior, err := q.CountSubsessionRunsThrough(ctx, rootRunID, run.ID, agent.ID); err == nil && prior > 0 {
			subRunNumber = prior
		}
		runKey += "-" + strconv.FormatInt(subRunNumber, 10)
	}
	run.Name = runKey
	if err := q.UpdateRunName(ctx, run.ID, runKey); err != nil {
		fmt.Printf("Warning: failed to store run name for run %d: %v\n", run.ID, err)
	}
	return run
}
