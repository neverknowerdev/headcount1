package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli/tools"
)

func (e *NativeEngine) createDurableSubtask(ctx context.Context, parent db.Task, p tools.DurableSubtaskParams) (string, error) {
	status := db.TaskStatusBacklog
	child, err := e.q.CreateTask(ctx, db.Task{
		CompanyID: parent.CompanyID, ProjectID: parent.ProjectID, SprintID: parent.SprintID,
		ParentID: &parent.ID, Title: strings.TrimSpace(p.Title), Description: p.Description,
		Status: status, Priority: parent.Priority, GitHubBranch: parent.GitHubBranch,
	})
	if err != nil {
		return "", fmt.Errorf("create durable subtask: %w", err)
	}
	rollback := func(cause error) (string, error) {
		_ = e.q.DeleteTaskRelationsForTask(ctx, child.ID)
		_ = e.q.DeleteTask(ctx, child.ID)
		return "", cause
	}
	for _, prerequisiteID := range p.DependsOnTaskIDs {
		if _, relationErr := e.q.CreateTaskRelation(ctx, db.TaskRelation{CompanyID: parent.CompanyID, SourceTaskID: child.ID, TargetTaskID: prerequisiteID, Kind: db.TaskRelationDependsOn}); relationErr != nil {
			return rollback(fmt.Errorf("failed to add dependency on task %d: %w", prerequisiteID, relationErr))
		}
	}
	for _, relatedID := range p.RelatedToTaskIDs {
		if _, relationErr := e.q.CreateTaskRelation(ctx, db.TaskRelation{CompanyID: parent.CompanyID, SourceTaskID: child.ID, TargetTaskID: relatedID, Kind: db.TaskRelationRelatedTo}); relationErr != nil {
			return rollback(fmt.Errorf("failed to relate task %d: %w", relatedID, relationErr))
		}
	}
	e.hub.BroadcastEventForCompany(child.CompanyID, "task_created", child)
	ref := child.RefKey
	if ref == "" {
		ref = fmt.Sprintf("#%d", child.ID)
	}
	return fmt.Sprintf("Subtask %s created as a durable planning record; no session was started.", ref), nil
}

type taskOperationalView struct {
	Task       db.Task              `json:"task"`
	Parent     *taskReference       `json:"parent,omitempty"`
	Children   []taskReference      `json:"children,omitempty"`
	Relations  []db.TaskRelation    `json:"relations,omitempty"`
	Comments   []db.Comment         `json:"comments,omitempty"`
	Runs       []runOperationalView `json:"runs,omitempty"`
	Truncation map[string]bool      `json:"truncation"`
}
type taskReference struct {
	ID     int32  `json:"id"`
	RefKey string `json:"ref_key,omitempty"`
	Title  string `json:"title"`
	Status string `json:"status"`
}
type runOperationalView struct {
	ID                   int32    `json:"id"`
	Kind                 string   `json:"kind"`
	Status               string   `json:"status"`
	Agent                string   `json:"agent"`
	StartedAt            string   `json:"started_at"`
	EndedAt              string   `json:"ended_at,omitempty"`
	ResultDescription    string   `json:"result_description,omitempty"`
	ResultExplanation    string   `json:"result_explanation,omitempty"`
	LatestReportedStatus string   `json:"latest_reported_status,omitempty"`
	LogTail              []string `json:"log_tail,omitempty"`
}

const maxOperationalComments = 100
const maxOperationalRuns = 50
const maxOperationalLogLines = 20

func (e *NativeEngine) getTaskOperationalView(ctx context.Context, companyID int32, reference string) (string, error) {
	var task db.Task
	if id, err := strconv.ParseInt(strings.TrimSpace(reference), 10, 32); err == nil {
		task, err = e.q.GetTask(ctx, int32(id))
		if err != nil {
			return "", err
		}
	} else {
		var lookupErr error
		task, lookupErr = e.q.GetTaskByRefKey(ctx, strings.TrimSpace(reference))
		if lookupErr != nil {
			return "", lookupErr
		}
	}
	if task.CompanyID != companyID {
		return "", fmt.Errorf("task %d is outside the current company", task.ID)
	}
	view := taskOperationalView{Task: task, Truncation: map[string]bool{"comments": false, "runs": false, "logs": false}}
	if task.ParentID != nil {
		if p, err := e.q.GetTask(ctx, *task.ParentID); err == nil {
			view.Parent = &taskReference{ID: p.ID, RefKey: p.RefKey, Title: p.Title, Status: p.Status}
		}
	}
	if children, err := e.q.ListSubtasksByParent(ctx, task.ID); err == nil {
		for _, child := range children {
			view.Children = append(view.Children, taskReference{ID: child.ID, RefKey: child.RefKey, Title: child.Title, Status: child.Status})
		}
	}
	view.Relations, _ = e.q.ListTaskRelations(ctx, task.ID)
	comments, err := e.q.ListCommentsByTask(ctx, task.ID)
	if err != nil {
		return "", err
	}
	if len(comments) > maxOperationalComments {
		view.Truncation["comments"] = true
		comments = comments[len(comments)-maxOperationalComments:]
	}
	view.Comments = comments
	runs, err := e.q.ListRunsByTask(ctx, task.ID)
	if err != nil {
		return "", err
	}
	if len(runs) > maxOperationalRuns {
		view.Truncation["runs"] = true
		runs = runs[len(runs)-maxOperationalRuns:]
	}
	for _, run := range runs {
		item := runOperationalView{ID: run.ID, Kind: run.Kind, Status: run.Status, Agent: run.Agent.Name, StartedAt: run.StartedAt.Format("2006-01-02T15:04:05Z07:00"), ResultDescription: run.ResultDescription, ResultExplanation: run.ResultExplanation, LatestReportedStatus: run.LatestReportedStatus}
		if run.EndedAt != nil {
			item.EndedAt = run.EndedAt.Format("2006-01-02T15:04:05Z07:00")
		}
		lines := strings.Split(run.LogContent, "\n")
		if len(lines) > maxOperationalLogLines {
			view.Truncation["logs"] = true
			lines = lines[len(lines)-maxOperationalLogLines:]
		}
		for _, line := range lines {
			if strings.TrimSpace(line) != "" {
				item.LogTail = append(item.LogTail, line)
			}
		}
		view.Runs = append(view.Runs, item)
	}
	b, err := json.Marshal(view)
	return string(b), err
}
