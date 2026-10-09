package endpoints

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agent-orchestrator/db"

	"github.com/go-chi/chi/v5"
)

// Every usage view reads the same ledger, one row per model call, so totals,
// breakdowns and the calls behind them always agree.

// usageDimensions are the ways usage can be grouped over the API.
var usageDimensions = map[string]bool{
	db.UsageByTask: true, db.UsageByRoot: true, db.UsageByPhase: true, db.UsageByPhaseTier: true,
	db.UsageByAgent: true, db.UsageByModel: true, db.UsageByTier: true, db.UsageByDay: true,
}

// parseUsageTime reads a date ("2026-10-08") or an RFC 3339 timestamp.
func parseUsageTime(value string) (*time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, true
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return &parsed, true
		}
	}
	return nil, false
}

// usageFilter builds the ledger filter of a request. The company comes from
// the caller, already authorized; the rest only narrows within it, so an id
// from another tenant matches nothing.
func usageFilter(r *http.Request, companyID int32) (db.UsageFilter, bool) {
	query := r.URL.Query()
	filter := db.UsageFilter{
		CompanyID:     companyID,
		Model:         query.Get("model"),
		Tier:          query.Get("tier"),
		WorkflowPhase: query.Get("phase"),
	}
	if value := query.Get("task_id"); value != "" {
		id, err := strconv.Atoi(value)
		if err != nil {
			return filter, false
		}
		taskID := int32(id)
		if query.Get("subtree") == "true" {
			filter.SubtreeOf = &taskID
		} else {
			filter.TaskID = &taskID
		}
	}
	if value := query.Get("agent_id"); value != "" {
		id, err := strconv.Atoi(value)
		if err != nil {
			return filter, false
		}
		agentID := int32(id)
		filter.AgentID = &agentID
	}
	var ok bool
	if filter.From, ok = parseUsageTime(query.Get("from")); !ok {
		return filter, false
	}
	if filter.To, ok = parseUsageTime(query.Get("to")); !ok {
		return filter, false
	}
	return filter, true
}

type usageResponse struct {
	Totals db.UsageTotals             `json:"totals"`
	Groups map[string][]db.UsageGroup `json:"groups"`
}

// usageReport sums a filter and breaks it down along each requested dimension.
func (api *API) usageReport(r *http.Request, filter db.UsageFilter, dimensions []string) (usageResponse, error) {
	totals, err := api.q.UsageTotals(r.Context(), filter)
	if err != nil {
		return usageResponse{}, err
	}
	report := usageResponse{Totals: totals, Groups: map[string][]db.UsageGroup{}}
	for _, dimension := range dimensions {
		groups, err := api.q.UsageBy(r.Context(), filter, dimension)
		if err != nil {
			return usageResponse{}, err
		}
		if groups == nil {
			groups = []db.UsageGroup{}
		}
		report.Groups[dimension] = groups
	}
	return report, nil
}

func (api *API) usageCompany(w http.ResponseWriter, r *http.Request) (int32, bool) {
	companyID, err := strconv.Atoi(r.URL.Query().Get("company_id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "company_id is required")
		return 0, false
	}
	if _, err := api.authorizeCompany(r, int32(companyID)); err != nil {
		api.respondError(w, http.StatusNotFound, "company not found")
		return 0, false
	}
	return int32(companyID), true
}

// GetUsage returns a company's usage: totals, and a breakdown for each
// dimension named in group_by (comma-separated: task, root_task, phase,
// phase_tier, agent, model, tier, day).
func (api *API) GetUsage(w http.ResponseWriter, r *http.Request) {
	companyID, ok := api.usageCompany(w, r)
	if !ok {
		return
	}
	filter, ok := usageFilter(r, companyID)
	if !ok {
		api.respondError(w, http.StatusBadRequest, "invalid usage filter")
		return
	}
	var dimensions []string
	for _, dimension := range strings.Split(r.URL.Query().Get("group_by"), ",") {
		dimension = strings.TrimSpace(dimension)
		if dimension == "" {
			continue
		}
		if !usageDimensions[dimension] {
			api.respondError(w, http.StatusBadRequest, "unknown group_by dimension "+dimension)
			return
		}
		dimensions = append(dimensions, dimension)
	}
	report, err := api.usageReport(r, filter, dimensions)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, report)
}

// GetTaskUsage returns what a task cost, including everything beneath it
// unless subtree=false, broken down by subtask, workflow step, agent and model.
func (api *API) GetTaskUsage(w http.ResponseWriter, r *http.Request) {
	task := api.taskFromCtx(r) // loaded + authorized by LoadTask
	filter := db.UsageFilter{CompanyID: task.CompanyID}
	taskID := task.ID
	if r.URL.Query().Get("subtree") == "false" {
		filter.TaskID = &taskID
	} else {
		filter.SubtreeOf = &taskID
	}
	report, err := api.usageReport(r, filter, []string{
		db.UsageByTask, db.UsageByPhase, db.UsageByPhaseTier, db.UsageByAgent, db.UsageByModel, db.UsageByTier,
	})
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, report)
}

// ListUsageCalls returns the individual calls behind any usage row, newest
// first. It takes the same filters as GetUsage; next_before, when present,
// is the before value of the next page.
func (api *API) ListUsageCalls(w http.ResponseWriter, r *http.Request) {
	companyID, ok := api.usageCompany(w, r)
	if !ok {
		return
	}
	filter, ok := usageFilter(r, companyID)
	if !ok {
		api.respondError(w, http.StatusBadRequest, "invalid usage filter")
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	calls, err := api.q.ListLLMCalls(r.Context(), filter, before, limit)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if calls == nil {
		calls = []db.LLMCall{}
	}
	response := map[string]interface{}{"calls": calls}
	if len(calls) == limit {
		response["next_before"] = calls[len(calls)-1].ID
	}
	api.respondJSON(w, http.StatusOK, response)
}

// usageCallResponse is one call with its log. A smart call carries the
// journal step that holds its full prompt and answer. An executor turn
// carries the entries of its session's log for that turn: what the model was
// given since its previous call, what it answered, and what its tools returned.
type usageCallResponse struct {
	Call    db.LLMCall        `json:"call"`
	Step    *db.TaskStep      `json:"step,omitempty"`
	Entries []json.RawMessage `json:"entries,omitempty"`
}

func (api *API) GetUsageCall(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid call id")
		return
	}
	call, err := api.q.GetLLMCall(r.Context(), id)
	if err != nil {
		api.respondError(w, http.StatusNotFound, "call not found")
		return
	}
	if _, err := api.authorizeCompany(r, call.CompanyID); err != nil {
		api.respondError(w, http.StatusNotFound, "call not found")
		return
	}
	response := usageCallResponse{Call: call}
	switch {
	case call.StepID != nil:
		if step, err := api.q.GetTaskStep(r.Context(), *call.StepID); err == nil {
			response.Step = &step
		}
	case call.RunID != nil && call.LogSeq != nil:
		entries, err := api.runTurnEntries(r, *call.RunID, *call.LogSeq)
		if err != nil {
			api.respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		response.Entries = entries
	}
	api.respondJSON(w, http.StatusOK, response)
}

// runTurnEntries returns the log entries of one executor turn: the call's
// request, its answer and the results of the tools that answer called, up to
// the next request. A session's first turn also carries what came before its
// first request: the brief. logSeq is the position of the call's answer in the
// session's log.
func (api *API) runTurnEntries(r *http.Request, runID int32, logSeq int64) ([]json.RawMessage, error) {
	previous, err := api.q.PreviousRunCallLogSeq(r.Context(), runID, logSeq)
	if err != nil {
		return nil, err
	}
	run, err := api.q.GetRun(r.Context(), runID)
	if err != nil {
		// The session was deleted; the call's figures remain.
		return nil, nil
	}
	var all []json.RawMessage
	if run.LogEntries == "" || json.Unmarshal([]byte(run.LogEntries), &all) != nil {
		return nil, nil
	}
	var turn []json.RawMessage
	// Some entries carry no position of their own; they belong where they
	// stand, right after the last entry that does.
	var position int64
	started := previous == 0
	for _, entry := range all {
		var head struct {
			Seq  int64  `json:"seq"`
			Type string `json:"type"`
		}
		if json.Unmarshal(entry, &head) != nil {
			continue
		}
		if head.Seq > 0 {
			position = head.Seq
		}
		if head.Type == "request" && position > previous {
			if position > logSeq {
				break // the next turn begins
			}
			started = true
		}
		if started {
			turn = append(turn, entry)
		}
	}
	return turn, nil
}
