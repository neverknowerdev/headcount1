package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/eventhub"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type questionRoundTripper func(*http.Request) (*http.Response, error)

func (f questionRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func questionClient(roundTripper questionRoundTripper) *aicli.Client {
	client := aicli.NewClient("http://question-test", "", "question-model")
	client.MaxRetries = 0
	client.HTTPClient = &http.Client{Transport: roundTripper}
	return client
}

func TestAnswerSessionQuestionReturnsAnswerAndPreservesPair(t *testing.T) {
	client := questionClient(func(req *http.Request) (*http.Response, error) {
		body, readErr := io.ReadAll(req.Body)
		require.NoError(t, readErr)
		var payload struct {
			Messages []aicli.Message `json:"messages"`
			Tools    []aicli.ToolDef `json:"tools"`
		}
		require.NoError(t, json.Unmarshal(body, &payload))
		require.Len(t, payload.Messages, 1)
		assert.Equal(t, "What happened?", payload.Messages[0].Content)
		assert.Empty(t, payload.Tools)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"the answer"}}]}`)), Header: http.Header{"Content-Type": {"application/json"}}}, nil
	})
	request := &sessionQuestionRequest{question: "What happened?", result: make(chan sessionQuestionResult, 1)}
	engine := &NativeEngine{}
	messages, err := engine.answerSessionQuestion(context.Background(), request, client, "")
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, "user", messages[0].Role)
	assert.Equal(t, "What happened?", messages[0].Content)
	assert.Equal(t, "the answer", messages[1].Content)
	result := <-request.result
	require.NoError(t, result.err)
	assert.Equal(t, "the answer", result.answer)
}

func TestAnswerSessionQuestionReturnsProviderErrorToWaitingOrchestrator(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	client := questionClient(func(*http.Request) (*http.Response, error) { return nil, providerErr })
	request := &sessionQuestionRequest{question: "Can you continue?", result: make(chan sessionQuestionResult, 1)}
	engine := &NativeEngine{}
	messages, err := engine.answerSessionQuestion(context.Background(), request, client, "")
	require.NoError(t, err)
	assert.Contains(t, messages[1].Content, "orchestrator question failed")
	result := <-request.result
	require.Error(t, result.err)
	assert.Contains(t, result.err.Error(), "provider unavailable")
}

func TestAnswerSessionQuestionPropagatesTimeoutError(t *testing.T) {
	client := questionClient(func(req *http.Request) (*http.Response, error) {
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	requestCtx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	request := &sessionQuestionRequest{question: "Are you blocked?", ctx: requestCtx, result: make(chan sessionQuestionResult, 1)}
	engine := &NativeEngine{}
	_, err := engine.answerSessionQuestion(context.Background(), request, client, "")
	require.NoError(t, err)
	result := <-request.result
	require.Error(t, result.err)
	assert.ErrorIs(t, result.err, context.DeadlineExceeded)
}

func TestOrchestratorAskSessionWaitsForExactSideChannelAnswer(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.Company{}, &db.Agent{}, &db.Project{}, &db.Sprint{}, &db.Task{}, &db.Run{}, &db.Comment{}, &db.RunEvent{}))
	company := db.Company{Name: "Acme", ShortName: "ACME"}
	require.NoError(t, database.Create(&company).Error)
	agent := db.Agent{CompanyID: company.ID, Name: "Worker", ShortName: "WRK"}
	require.NoError(t, database.Create(&agent).Error)
	task := db.Task{CompanyID: company.ID, AgentID: &agent.ID, Title: "Question task"}
	require.NoError(t, database.Create(&task).Error)
	orchestrator := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "running"}
	require.NoError(t, database.Create(&orchestrator).Error)
	rootID := orchestrator.ID
	require.NoError(t, database.Model(&db.Run{}).Where("id = ?", orchestrator.ID).Update("root_run_id", rootID).Error)
	worker := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "running", ParentRunID: &rootID, RootRunID: &rootID}
	require.NoError(t, database.Create(&worker).Error)

	engine := NewNativeEngine(database, eventhub.NewHub())
	questionBroker := newSessionQuestionBroker()
	engine.sessionQuestionChans.Store(worker.ID, questionBroker)
	defer engine.sessionQuestionChans.Delete(worker.ID)
	go func() {
		request := <-questionBroker.ch
		request.result <- sessionQuestionResult{answer: "exact answer"}
	}()
	answer, err := engine.orchestratorAskSession(context.Background(), task, orchestrator.ID, worker.ID, "What is the exact state?")
	require.NoError(t, err)
	assert.Equal(t, "exact answer", answer)
	var comments []db.Comment
	require.NoError(t, database.Where("comment_type = ?", "orchestrator_question").Find(&comments).Error)
	require.Len(t, comments, 1)
}

func TestOrchestratorAskSessionReturnsTimeoutAsError(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.Company{}, &db.Agent{}, &db.Task{}, &db.Run{}, &db.Comment{}))
	company := db.Company{Name: "Acme", ShortName: "ACME"}
	require.NoError(t, database.Create(&company).Error)
	agent := db.Agent{CompanyID: company.ID, Name: "Worker"}
	require.NoError(t, database.Create(&agent).Error)
	task := db.Task{CompanyID: company.ID, AgentID: &agent.ID, Title: "Question task"}
	require.NoError(t, database.Create(&task).Error)
	orchestrator := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "running"}
	require.NoError(t, database.Create(&orchestrator).Error)
	rootID := orchestrator.ID
	require.NoError(t, database.Model(&db.Run{}).Where("id = ?", orchestrator.ID).Update("root_run_id", rootID).Error)
	worker := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "running", ParentRunID: &rootID, RootRunID: &rootID}
	require.NoError(t, database.Create(&worker).Error)

	engine := NewNativeEngine(database, eventhub.NewHub())
	engine.sessionQuestionChans.Store(worker.ID, newSessionQuestionBroker())
	defer engine.sessionQuestionChans.Delete(worker.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = engine.orchestratorAskSession(ctx, task, orchestrator.ID, worker.ID, "Will this time out?")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}
