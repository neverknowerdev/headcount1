package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"agent-orchestrator/engine/aicli"
)

// CreateTaskParams carries the parameters of one create_task call.
type CreateTaskParams struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Priority    string `json:"priority"`
	TaskType    string `json:"task_type"`
	SprintID    int32  `json:"sprint_id"`
	ProjectID   int32  `json:"project_id"`
	DueDate     string `json:"due_date"`
	// AgentName is the database Agent role key or display name to assign.
	AgentName        string  `json:"agent_name"`
	DependsOnTaskIDs []int32 `json:"depends_on_task_ids"`
	RelatedToTaskIDs []int32 `json:"related_to_task_ids"`
}

// CreateTask creates a new TOP-LEVEL task on the board, alongside the tasks
// humans create. Unlike create_subtask it does not run anything and does not
// block: the task lives its own lifecycle (picked up when moved to / created
// in "to-do"). Used by the orchestrator for planning: turning decisions into
// scheduled work.
type CreateTask struct {
	fn func(ctx context.Context, p CreateTaskParams) (string, error)
}

func NewCreateTask(fn func(ctx context.Context, p CreateTaskParams) (string, error)) *CreateTask {
	return &CreateTask{fn: fn}
}

func (t *CreateTask) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: string(aicli.ToolCreateTask),
			Description: "Create a new TOP-LEVEL task on the board (a sibling of the current task, not a subtask). " +
				"Use it for planning: recording decided-on work as separate tasks with their own lifecycle. " +
				"The call returns immediately — nothing is executed now. Tasks created in \"backlog\" (default) wait for " +
				"scheduling; tasks created in \"to-do\" start executing right away, independently of your session.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"title":{
						"type":"string",
						"description":"Short task title"
					},
					"description":{
						"type":"string",
						"description":"Full task description: goal, context, and what the expected result looks like"
					},
					"status":{
						"type":"string",
						"enum":["backlog","to-do"],
						"description":"Initial board column. \"backlog\" (default) = planned, not started; \"to-do\" = start executing immediately."
					},
					"priority":{
						"type":"string",
						"enum":["Low","Normal","High","Urgent"],
						"description":"Task priority (default Normal)"
					},
					"task_type":{
						"type":"string",
						"enum":["plan and implement","implement"],
						"description":"Task type (default \"plan and implement\")"
					},
					"sprint_id":{
						"type":"integer",
						"description":"Sprint to place the task in (default: the current task's sprint)"
					},
					"project_id":{
						"type":"integer",
						"description":"Project to attach the task to (default: the current task's project)"
					},
					"due_date":{
						"type":"string",
						"description":"Optional due date, RFC3339 (e.g. \"2026-08-01T00:00:00Z\")"
					},
					"agent_name":{
						"type":"string",
						"description":"Optional database agent role key or name to assign (for example \"CTO\")"
					},
					"depends_on_task_ids":{
						"type":"array","items":{"type":"integer"},
						"description":"Existing tasks that must be done before this task can start"
					},
					"related_to_task_ids":{
						"type":"array","items":{"type":"integer"},
						"description":"Existing tasks that are informationally related"
					}
				},
				"required":["title","description"]
			}`),
		},
	}
}

func (t *CreateTask) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p CreateTaskParams
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("create_task: %w", err)
	}
	if p.Title == "" {
		return "", fmt.Errorf("title is required")
	}
	if p.Description == "" {
		return "", fmt.Errorf("description is required")
	}
	return t.fn(ctx, p)
}
