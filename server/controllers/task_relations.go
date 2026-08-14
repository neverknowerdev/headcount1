package endpoints

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"agent-orchestrator/db"

	"github.com/go-chi/chi/v5"
)

type taskRelationResponse struct {
	DependsOn []db.TaskRelationView `json:"depends_on"`
	BlockedBy []db.TaskRelationView `json:"blocked_by"`
	Blocks    []db.TaskRelationView `json:"blocks"`
	RelatedTo []db.TaskRelationView `json:"related_to"`
}

func (api *API) ListTaskRelations(w http.ResponseWriter, r *http.Request) {
	task := api.taskFromCtx(r)
	relations, err := api.q.ListTaskRelations(r.Context(), task.ID)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	response, err := api.buildTaskRelationResponse(r.Context(), task, relations)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, response)
}

func (api *API) CreateTaskRelation(w http.ResponseWriter, r *http.Request) {
	task := api.taskFromCtx(r)
	var req struct {
		Type   string `json:"type"`
		TaskID int32  `json:"task_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "invalid request payload")
		return
	}
	if req.TaskID == 0 {
		api.respondError(w, http.StatusBadRequest, "task_id is required")
		return
	}

	rawType := strings.TrimSpace(strings.ToLower(req.Type))
	kind := rawType
	sourceID, targetID := task.ID, req.TaskID
	if rawType == "blocks" {
		kind = db.TaskRelationDependsOn
		sourceID, targetID = req.TaskID, task.ID
	}
	if kind != db.TaskRelationDependsOn && kind != db.TaskRelationRelatedTo {
		api.respondError(w, http.StatusBadRequest, "type must be depends_on, blocks, or related_to")
		return
	}

	target, err := api.q.GetTask(r.Context(), req.TaskID)
	if err != nil || target.CompanyID != task.CompanyID {
		api.respondError(w, http.StatusNotFound, "related task not found")
		return
	}
	relation, err := api.q.CreateTaskRelation(r.Context(), db.TaskRelation{
		CompanyID:    task.CompanyID,
		SourceTaskID: sourceID,
		TargetTaskID: targetID,
		Kind:         kind,
	})
	if err != nil {
		status := http.StatusBadRequest
		if isRelationConflict(err) {
			status = http.StatusConflict
		}
		api.respondError(w, status, err.Error())
		return
	}

	api.hub.BroadcastEventForCompany(task.CompanyID, "task_updated", map[string]interface{}{"id": sourceID, "relation_changed": true})
	api.hub.BroadcastEventForCompany(task.CompanyID, "task_updated", map[string]interface{}{"id": targetID, "relation_changed": true})
	if kind == db.TaskRelationDependsOn {
		go func() { _ = api.engine.ProcessTask(context.Background(), sourceID) }()
	}
	api.respondJSON(w, http.StatusCreated, relation)
}

func (api *API) DeleteTaskRelation(w http.ResponseWriter, r *http.Request) {
	task := api.taskFromCtx(r)
	relationID := int32(0)
	if _, err := fmt.Sscanf(chi.URLParam(r, "relationID"), "%d", &relationID); err != nil || relationID == 0 {
		api.respondError(w, http.StatusBadRequest, "invalid relation id")
		return
	}
	relation, err := api.q.GetTaskRelation(r.Context(), relationID)
	if err != nil || (relation.SourceTaskID != task.ID && relation.TargetTaskID != task.ID) {
		api.respondError(w, http.StatusNotFound, "relation not found")
		return
	}
	// The route has already authorized the current task. Authorize the other
	// endpoint as well so a malformed relation can never cross a tenant.
	otherID := relation.SourceTaskID
	if otherID == task.ID {
		otherID = relation.TargetTaskID
	}
	other, err := api.q.GetTask(r.Context(), otherID)
	if err != nil || other.CompanyID != task.CompanyID {
		api.respondError(w, http.StatusNotFound, "relation not found")
		return
	}
	if err := api.q.DeleteTaskRelation(r.Context(), relationID); err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.hub.BroadcastEventForCompany(task.CompanyID, "task_updated", map[string]interface{}{"id": relation.SourceTaskID, "relation_changed": true})
	api.hub.BroadcastEventForCompany(task.CompanyID, "task_updated", map[string]interface{}{"id": relation.TargetTaskID, "relation_changed": true})
	if relation.Kind == db.TaskRelationDependsOn {
		go func() { _ = api.engine.ProcessTask(context.Background(), relation.SourceTaskID) }()
	}
	w.WriteHeader(http.StatusNoContent)
}

func (api *API) buildTaskRelationResponse(ctx context.Context, task db.Task, relations []db.TaskRelation) (taskRelationResponse, error) {
	response := taskRelationResponse{}
	if len(relations) == 0 {
		return response, nil
	}
	ids := make([]int32, 0, len(relations)*2)
	seen := make(map[int32]struct{}, len(relations)*2)
	for _, relation := range relations {
		for _, id := range []int32{relation.SourceTaskID, relation.TargetTaskID} {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	var tasks []db.Task
	if err := api.db.WithContext(ctx).Where("id IN ?", ids).Find(&tasks).Error; err != nil {
		return response, err
	}
	byID := make(map[int32]db.Task, len(tasks))
	for _, item := range tasks {
		byID[item.ID] = item
	}
	compact := func(relationID int32, item db.Task) db.TaskRelationView {
		return db.TaskRelationView{RelationID: relationID, Task: db.TaskRelationTask{ID: item.ID, RefKey: item.RefKey, Title: item.Title, Status: item.Status}}
	}
	for _, relation := range relations {
		var peer db.Task
		var ok bool
		switch {
		case relation.Kind == db.TaskRelationDependsOn && relation.SourceTaskID == task.ID:
			peer, ok = byID[relation.TargetTaskID]
			if ok {
				response.DependsOn = append(response.DependsOn, compact(relation.ID, peer))
				if peer.Status != db.TaskStatusDone {
					response.BlockedBy = append(response.BlockedBy, compact(relation.ID, peer))
				}
			}
		case relation.Kind == db.TaskRelationDependsOn && relation.TargetTaskID == task.ID:
			peer, ok = byID[relation.SourceTaskID]
			if ok {
				response.Blocks = append(response.Blocks, compact(relation.ID, peer))
			}
		case relation.Kind == db.TaskRelationRelatedTo && relation.SourceTaskID == task.ID:
			peer, ok = byID[relation.TargetTaskID]
			if ok {
				response.RelatedTo = append(response.RelatedTo, compact(relation.ID, peer))
			}
		case relation.Kind == db.TaskRelationRelatedTo && relation.TargetTaskID == task.ID:
			peer, ok = byID[relation.SourceTaskID]
			if ok {
				response.RelatedTo = append(response.RelatedTo, compact(relation.ID, peer))
			}
		}
	}
	sortRelationViews(response.DependsOn)
	sortRelationViews(response.BlockedBy)
	sortRelationViews(response.Blocks)
	sortRelationViews(response.RelatedTo)
	return response, nil
}

func sortRelationViews(values []db.TaskRelationView) {
	sort.Slice(values, func(i, j int) bool { return values[i].Task.ID < values[j].Task.ID })
}

func isRelationConflict(err error) bool {
	message := strings.ToLower(err.Error())
	for _, fragment := range []string{"already exists", "cycle", "different companies", "active run", "hierarchy"} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}
