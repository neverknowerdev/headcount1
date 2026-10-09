package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/db/migrations"
	"agent-orchestrator/engine"
	"agent-orchestrator/engine/enginetest"
	"agent-orchestrator/eventhub"
	endpoints "agent-orchestrator/server/controllers"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// TestUpdateTaskDoesNotOverwriteConcurrentEngineWrite reproduces the race the
// handler used to lose: the task is loaded by middleware, the engine then
// changes it, and the handler saves. An edit to one field must not write the
// stale copy of every other field back over the engine's change.
func TestUpdateTaskDoesNotOverwriteConcurrentEngineWrite(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	q := db.New(database)
	uid := testSeedUserID(t, q)
	company := db.Company{Name: "Acme", ShortName: "acme", UserID: &uid}
	require.NoError(t, database.Create(&company).Error)
	sprint := db.Sprint{CompanyID: company.ID, Name: "Sprint 1"}
	require.NoError(t, database.Create(&sprint).Error)
	task, err := q.CreateTask(context.Background(), db.Task{
		CompanyID: company.ID, SprintID: sprint.ID, Title: "before", Status: db.TaskStatusTodo,
	})
	require.NoError(t, err)

	api := endpoints.NewAPI(database, &enginetest.Recorder{}, eventhub.NewHub())
	r := chi.NewRouter()
	r.Route("/tasks/{id}", func(r chi.Router) {
		r.Use(api.LoadTask)
		// Runs after the task has been loaded into the request and before the
		// handler writes: exactly where an engine transition can land.
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				require.NoError(t, database.Model(&db.Task{}).Where("id = ?", task.ID).
					Updates(map[string]interface{}{"status": db.TaskStatusInProgress, "run_id": 55}).Error)
				next.ServeHTTP(w, req)
			})
		})
		r.Put("/", api.UpdateTask)
	})
	router := withTestUser(t, database, r)

	body, _ := json.Marshal(map[string]any{"title": "after", "priority": "High"})
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/tasks/%d", task.ID), bytes.NewReader(body))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	var response db.Task
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Equal(t, "after", response.Title)
	require.Equal(t, db.TaskStatusInProgress, response.Status, "the response reflects the row as written, not the stale copy")

	persisted, err := q.GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	require.Equal(t, "after", persisted.Title)
	require.Equal(t, "High", persisted.Priority)
	require.Equal(t, db.TaskStatusInProgress, persisted.Status, "the engine's status change must survive the edit")
	require.NotNil(t, persisted.RunID, "the engine's run must survive the edit")
	require.Equal(t, int32(55), *persisted.RunID)
	require.Equal(t, sprint.ID, persisted.SprintID)
}

// taskAPIFixture is a company with one sprint behind the task routes, driven
// by the real engine. The engine is not started, so nothing advances on its
// own: a task stays where a request left it, which is what these tests look at.
type taskAPIFixture struct {
	t        *testing.T
	database *gorm.DB
	q        *db.Queries
	router   chi.Router
	company  db.Company
	sprint   db.Sprint
	userID   int32
}

func newTaskAPIFixture(t *testing.T) *taskAPIFixture {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	f := &taskAPIFixture{t: t, database: database, q: db.New(database)}
	f.userID = testSeedUserID(t, f.q)
	f.company = db.Company{Name: "Acme", ShortName: "acme", UserID: &f.userID}
	require.NoError(t, database.Create(&f.company).Error)
	f.sprint = db.Sprint{CompanyID: f.company.ID, Name: "Sprint 1"}
	require.NoError(t, database.Create(&f.sprint).Error)

	hub := eventhub.NewHub()
	api := endpoints.NewAPI(database, engine.NewNativeEngine(database, hub), hub)
	r := chi.NewRouter()
	r.Post("/tasks", api.CreateTask)
	r.Route("/tasks/{id}", func(r chi.Router) {
		r.Use(api.LoadTask)
		r.Get("/", api.GetTask)
		r.Put("/", api.UpdateTask)
		r.Post("/rerun", api.RerunTask)
		r.Post("/stop", api.StopTask)
		r.Get("/tree", api.ListTaskTree)
		r.Get("/steps", api.ListTaskSteps)
		r.Get("/steps/{stepID}", api.GetTaskStep)
		r.Get("/decisions", api.ListTaskDecisions)
	})
	f.router = withTestUser(t, database, r)
	return f
}

func (f *taskAPIFixture) do(method, path string, body any) *httptest.ResponseRecorder {
	f.t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		require.NoError(f.t, err)
		reader = bytes.NewReader(encoded)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, httptest.NewRequest(method, path, reader))
	return w
}

func (f *taskAPIFixture) create(body map[string]any) db.Task {
	f.t.Helper()
	body["company_id"] = f.company.ID
	body["sprint_id"] = f.sprint.ID
	w := f.do(http.MethodPost, "/tasks", body)
	require.Equal(f.t, http.StatusCreated, w.Code, w.Body.String())
	var task db.Task
	require.NoError(f.t, json.Unmarshal(w.Body.Bytes(), &task))
	return task
}

func (f *taskAPIFixture) task(id int32) db.Task {
	f.t.Helper()
	task, err := f.q.GetTask(context.Background(), id)
	require.NoError(f.t, err)
	return task
}

// A task's status says what the workflow is doing with it, so a person does
// not set it freely: they queue the task, stop it, and place it once it is at
// rest. Nothing a person does through the API leaves a wait behind.
func TestTaskStatusIsMovedThroughTheWorkflow(t *testing.T) {
	f := newTaskAPIFixture(t)
	created := f.create(map[string]any{"title": "write the report", "task_type": "research"})
	require.Equal(t, db.TaskStatusBacklog, created.Status)
	require.Equal(t, "research", created.TaskType)
	require.Equal(t, db.TaskModeManaged, created.Mode)
	path := fmt.Sprintf("/tasks/%d", created.ID)

	// Statuses that describe the workflow's own progress cannot be set by hand.
	for _, status := range []string{db.TaskStatusInProgress, db.TaskStatusBlocked, db.TaskStatusDependsOnTask, db.TaskStatusFailed, db.TaskStatusCanceled, "refinement"} {
		w := f.do(http.MethodPut, path, map[string]any{"status": status})
		require.Equal(t, http.StatusConflict, w.Code, "status %s: %s", status, w.Body.String())
	}
	require.Equal(t, db.TaskStatusBacklog, f.task(created.ID).Status)

	// Queueing starts it.
	w := f.do(http.MethodPut, path, map[string]any{"status": db.TaskStatusTodo})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, db.TaskStatusTodo, f.task(created.ID).Status)

	// While it is queued or running it cannot be moved, retyped or archived,
	// and a refused move writes nothing else from the same request.
	w = f.do(http.MethodPut, path, map[string]any{"status": db.TaskStatusDone, "title": "sneaky"})
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "stop it first")
	require.Equal(t, "write the report", f.task(created.ID).Title)
	w = f.do(http.MethodPut, path, map[string]any{"task_type": "coding"})
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	w = f.do(http.MethodPut, path, map[string]any{"is_archived": true})
	require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
	// Other edits are fine while it runs.
	w = f.do(http.MethodPut, path, map[string]any{"priority": "High"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// Stopping parks it, visibly, until someone runs it again.
	w = f.do(http.MethodPost, path+"/stop", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	stopped := f.task(created.ID)
	require.Equal(t, db.TaskStatusBlocked, stopped.Status)
	require.Equal(t, "operator", stopped.WaitingOn)
	require.Equal(t, "High", stopped.Priority)

	// At rest it can be placed, and nothing is left waiting.
	w = f.do(http.MethodPut, path, map[string]any{"status": db.TaskStatusDone})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	done := f.task(created.ID)
	require.Equal(t, db.TaskStatusDone, done.Status)
	require.Empty(t, done.WaitingOn)
	require.Empty(t, done.Phase)
	require.NotNil(t, done.DoneAt)
	w = f.do(http.MethodPut, path, map[string]any{"task_type": "coding", "is_archived": true})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "coding", f.task(created.ID).TaskType)

	// Each move a person made is on the task's record.
	var moves []db.Comment
	require.NoError(t, f.database.Where("task_id = ? AND comment_type = ?", created.ID, "status_change").Order("id").Find(&moves).Error)
	require.Len(t, moves, 3)
	require.JSONEq(t, `{"from":"backlog","to":"to-do"}`, moves[0].Content)
	require.JSONEq(t, `{"from":"to-do","to":"blocked"}`, moves[1].Content)
	require.JSONEq(t, `{"from":"blocked","to":"done"}`, moves[2].Content)
	for _, move := range moves {
		require.Equal(t, "human", move.AuthorType)
	}

	// Running a finished task again sends it back to work.
	w = f.do(http.MethodPost, path+"/rerun", nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	rerun := f.task(created.ID)
	require.Equal(t, db.TaskStatusInProgress, rerun.Status)
	require.NotEmpty(t, rerun.Phase)
}

func TestCreateTaskRejectsUnknownType(t *testing.T) {
	f := newTaskAPIFixture(t)
	w := f.do(http.MethodPost, "/tasks", map[string]any{"company_id": f.company.ID, "sprint_id": f.sprint.ID, "title": "x", "task_type": "plan and implement"})
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	require.Equal(t, db.TaskTypeGeneral, f.create(map[string]any{"title": "untyped"}).TaskType)
}

// A task may name its own model: a provider with a model, or a model group.
// It must be the caller's own; another tenant's provider is never bound.
func TestTaskModelOverride(t *testing.T) {
	f := newTaskAPIFixture(t)
	provider := db.LLMProvider{Name: "Mine", BaseUrl: "https://mine.test", UserID: &f.userID}
	require.NoError(t, f.database.Create(&provider).Error)
	group := db.ModelGroup{Name: "Mine", Slug: "mine", UserID: &f.userID}
	require.NoError(t, f.database.Create(&group).Error)
	stranger, err := f.q.CreateUser(context.Background(), "stranger@test.local")
	require.NoError(t, err)
	foreign := db.LLMProvider{Name: "Theirs", BaseUrl: "https://theirs.test", UserID: &stranger.ID}
	require.NoError(t, f.database.Create(&foreign).Error)

	w := f.do(http.MethodPost, "/tasks", map[string]any{"company_id": f.company.ID, "sprint_id": f.sprint.ID, "title": "x", "provider_id": foreign.ID, "model": "m"})
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())

	created := f.create(map[string]any{"title": "with a model", "provider_id": provider.ID, "model": "strong-model"})
	require.NotNil(t, created.ProviderID)
	require.Equal(t, provider.ID, *created.ProviderID)
	require.Equal(t, "strong-model", created.Model)
	path := fmt.Sprintf("/tasks/%d", created.ID)

	w = f.do(http.MethodPut, path, map[string]any{"provider_id": foreign.ID, "model": "m"})
	require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
	require.Equal(t, "strong-model", f.task(created.ID).Model)

	// An unrelated edit leaves the override alone.
	w = f.do(http.MethodPut, path, map[string]any{"title": "renamed"})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "strong-model", f.task(created.ID).Model)

	// A model group replaces the provider and its model.
	w = f.do(http.MethodPut, path, map[string]any{"model_group_id": group.ID})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	grouped := f.task(created.ID)
	require.NotNil(t, grouped.ModelGroupID)
	require.Equal(t, group.ID, *grouped.ModelGroupID)
	require.Nil(t, grouped.ProviderID)
	require.Empty(t, grouped.Model)

	// An empty model clears the override: the task uses its tier's default.
	w = f.do(http.MethodPut, path, map[string]any{"model": ""})
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	cleared := f.task(created.ID)
	require.Nil(t, cleared.ProviderID)
	require.Nil(t, cleared.ModelGroupID)
	require.Empty(t, cleared.Model)
}

// The journal, the decisions and the task tree are read per task. A step's
// full prompt is fetched one step at a time, and never through another task.
func TestTaskJournalDecisionsAndTree(t *testing.T) {
	f := newTaskAPIFixture(t)
	ctx := context.Background()
	root := f.create(map[string]any{"title": "root"})
	other := f.create(map[string]any{"title": "other"})
	step, err := f.q.AppendTaskStep(ctx, db.TaskStep{TaskID: root.ID, RootTaskID: root.ID, Kind: "smart_call", Phase: "plan", Prompt: "the whole prompt", Response: "the whole answer", Result: "created 1 task"})
	require.NoError(t, err)
	child, err := f.q.CreateTask(ctx, db.Task{CompanyID: f.company.ID, SprintID: f.sprint.ID, ParentID: &root.ID, Title: "child", OriginStepID: &step.ID})
	require.NoError(t, err)
	_, err = f.q.CreateDecision(ctx, db.Decision{TaskID: root.ID, RootTaskID: root.ID, StepID: &step.ID, Kind: "decision", Title: "split in two"})
	require.NoError(t, err)
	_, err = f.q.CreateDecision(ctx, db.Decision{TaskID: child.ID, RootTaskID: root.ID, Kind: "dead_end", Title: "the old API"})
	require.NoError(t, err)

	w := f.do(http.MethodGet, fmt.Sprintf("/tasks/%d/steps", root.ID), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var steps []db.TaskStep
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &steps))
	require.Len(t, steps, 1)
	require.Equal(t, "created 1 task", steps[0].Result)
	require.Empty(t, steps[0].Prompt, "the list leaves the bulky prompt out")

	w = f.do(http.MethodGet, fmt.Sprintf("/tasks/%d/steps/%d", root.ID, step.ID), nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var full db.TaskStep
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &full))
	require.Equal(t, "the whole prompt", full.Prompt)
	require.Equal(t, "the whole answer", full.Response)
	w = f.do(http.MethodGet, fmt.Sprintf("/tasks/%d/steps/%d", other.ID, step.ID), nil)
	require.Equal(t, http.StatusNotFound, w.Code, "a step is only readable through its own task")

	var decisions []db.Decision
	w = f.do(http.MethodGet, fmt.Sprintf("/tasks/%d/decisions", root.ID), nil)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &decisions))
	require.Len(t, decisions, 1)
	w = f.do(http.MethodGet, fmt.Sprintf("/tasks/%d/decisions?subtree=true", root.ID), nil)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &decisions))
	require.Len(t, decisions, 2)
	w = f.do(http.MethodGet, fmt.Sprintf("/tasks/%d/decisions?subtree=true", child.ID), nil)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &decisions))
	require.Len(t, decisions, 1)
	require.Equal(t, "the old API", decisions[0].Title)

	var tree []db.Task
	w = f.do(http.MethodGet, fmt.Sprintf("/tasks/%d/tree", root.ID), nil)
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &tree))
	require.Len(t, tree, 2)
	require.Equal(t, root.ID, tree[0].ID)
	require.Equal(t, child.ID, tree[1].ID)
}
