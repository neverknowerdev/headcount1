package endpoints

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/db/models"
)

// Kinds of error a task's tree can meet.
const (
	// errorKindModelCall: a model could not be called.
	errorKindModelCall = "model_call"
	// errorKindRejected: a model answered, but not with something usable.
	errorKindRejected = "rejected_answer"
	// errorKindSession: an executor session ended without reporting.
	errorKindSession = "session"
)

// maxErrorCalls bounds how many failed calls one report reads. A tree that
// failed more often than this is showing the same few errors over and over.
const maxErrorCalls = 500

// taskErrorGroup is one error as it occurred, however many times, anywhere
// beneath a task.
type taskErrorGroup struct {
	Kind     string    `json:"kind"`
	Message  string    `json:"message"`
	Count    int       `json:"count"`
	FirstAt  time.Time `json:"first_at"`
	LastAt   time.Time `json:"last_at"`
	Provider string    `json:"provider,omitempty"`
	Model    string    `json:"model,omitempty"`
	Tier     string    `json:"tier,omitempty"`
	// CallID is the most recent failed call of the group, to open as a log.
	CallID int64 `json:"call_id,omitempty"`
	// Tasks are the tasks it happened in, most recent first.
	Tasks []taskErrorTask `json:"tasks"`
}

type taskErrorTask struct {
	ID     int32  `json:"id"`
	RefKey string `json:"ref_key"`
	Title  string `json:"title"`
}

// taskErrorReport is everything that went wrong beneath a task that was not
// the work's own outcome: models that could not be called, answers that could
// not be used, sessions that crashed. An executor reporting that it failed is
// a result, not an error, and is not listed.
type taskErrorReport struct {
	Total  int              `json:"total"`
	Groups []taskErrorGroup `json:"groups"`
}

// ListTaskErrors reports the errors met by a task and everything beneath it,
// identical ones counted together, the most recent first.
func (api *API) ListTaskErrors(w http.ResponseWriter, r *http.Request) {
	task := api.taskFromCtx(r) // loaded + authorized by LoadTask
	taskID := task.ID
	tree, err := api.q.ListTaskSubtree(r.Context(), taskID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	byID := make(map[int32]db.Task, len(tree))
	for _, member := range tree {
		byID[member.ID] = member
	}
	var calls []db.LLMCall
	for before := int64(0); len(calls) < maxErrorCalls; {
		page, err := api.q.ListLLMCalls(r.Context(), db.UsageFilter{CompanyID: task.CompanyID, SubtreeOf: &taskID, FailedOnly: true}, before, 500)
		if err != nil {
			api.respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		calls = append(calls, page...)
		if len(page) < 500 {
			break
		}
		before = page[len(page)-1].ID
	}

	report := taskErrorReport{Groups: []taskErrorGroup{}}
	index := map[string]int{}
	add := func(group taskErrorGroup, at time.Time, taskID int32) {
		key := group.Kind + "\x00" + group.Provider + "\x00" + group.Model + "\x00" + group.Message
		i, seen := index[key]
		if !seen {
			i = len(report.Groups)
			index[key] = i
			group.FirstAt, group.LastAt, group.Tasks = at, at, []taskErrorTask{}
			report.Groups = append(report.Groups, group)
		}
		entry := &report.Groups[i]
		entry.Count++
		report.Total++
		if at.Before(entry.FirstAt) {
			entry.FirstAt = at
		}
		if at.After(entry.LastAt) {
			entry.LastAt = at
		}
		if entry.CallID == 0 {
			entry.CallID = group.CallID
		}
		for _, listed := range entry.Tasks {
			if listed.ID == taskID {
				return
			}
		}
		if member, ok := byID[taskID]; ok {
			entry.Tasks = append(entry.Tasks, taskErrorTask{ID: member.ID, RefKey: member.RefKey, Title: member.Title})
		}
	}

	// Calls arrive newest first, so the first call of a group is its latest.
	for _, call := range calls {
		kind := errorKindModelCall
		if call.Status == models.LLMCallRejected {
			kind = errorKindRejected
		}
		model := call.RequestedModel
		if model == "" {
			model = call.Model
		}
		var of int32
		if call.TaskID != nil {
			of = *call.TaskID
		}
		add(taskErrorGroup{Kind: kind, Message: errorMessage(call.Error), Provider: call.ProviderName, Model: model, Tier: call.Tier, CallID: call.ID}, call.CreatedAt, of)
	}
	// Sessions that ended without a report and not because of their model:
	// those are already listed by the call that failed.
	for i := len(tree) - 1; i >= 0; i-- {
		member := tree[i]
		if member.Status == db.TaskStatusFailed && member.ResultReason == models.TaskResultRunError {
			add(taskErrorGroup{Kind: errorKindSession, Message: errorMessage(member.ResultSummary)}, member.UpdatedAt, member.ID)
		}
	}
	sort.SliceStable(report.Groups, func(i, j int) bool { return report.Groups[i].LastAt.After(report.Groups[j].LastAt) })
	api.respondJSON(w, http.StatusOK, report)
}

// errorMessage is an error as it is shown and compared: without the wrapping
// that differs from one occurrence to the next.
func errorMessage(text string) string {
	text = strings.TrimSpace(text)
	if _, after, found := strings.Cut(text, "LLM call failed: "); found {
		text = after
	}
	if text == "" {
		return "no error message was recorded"
	}
	return strings.Join(strings.Fields(text), " ")
}
