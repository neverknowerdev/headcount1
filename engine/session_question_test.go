package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
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
		require.Len(t, payload.Messages, 3)
		assert.Equal(t, "Earlier worker context", payload.Messages[0].Content)
		assert.Equal(t, "Previous worker answer", payload.Messages[1].Content)
		assert.Equal(t, "What happened?", payload.Messages[2].Content)
		assert.Empty(t, payload.Tools)
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"role":"assistant","content":"the answer"}}]}`)), Header: http.Header{"Content-Type": {"application/json"}}}, nil
	})
	request := &sessionQuestionRequest{question: "What happened?", result: make(chan sessionQuestionResult, 1)}
	engine := &NativeEngine{}
	messages, err := engine.answerSessionQuestion(context.Background(), request, client, "", []aicli.Message{
		{Role: "user", Content: "Earlier worker context"},
		{Role: "assistant", Content: "Previous worker answer"},
	})
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.Equal(t, "user", messages[0].Role)
	assert.Equal(t, "What happened?", messages[0].Content)
	assert.Equal(t, "the answer", messages[1].Content)
	result := <-request.result
	require.NoError(t, result.err)
	assert.Equal(t, "the answer", result.answer)
}

func TestSessionQuestionBrokerCloseTerminatesAndRejectsNewQuestions(t *testing.T) {
	broker := newSessionQuestionBroker()
	assert.EqualError(t, broker.submit(context.Background(), nil), "session question request is nil")
	request := &sessionQuestionRequest{result: make(chan sessionQuestionResult, 1)}
	require.NoError(t, broker.submit(context.Background(), request))

	closed := make(chan struct{})
	go func() {
		broker.close(errors.New("session ended"))
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("broker close did not return")
	}

	result := <-request.result
	assert.EqualError(t, result.err, "session ended")
	_, open := broker.receive()
	assert.False(t, open)
	assert.Error(t, broker.submit(context.Background(), &sessionQuestionRequest{result: make(chan sessionQuestionResult, 1)}))
}

func TestSessionQuestionBrokerCloseUnblocksFullQueueSubmit(t *testing.T) {
	broker := newSessionQuestionBroker()
	for i := 0; i < cap(broker.ch); i++ {
		require.NoError(t, broker.submit(context.Background(), &sessionQuestionRequest{result: make(chan sessionQuestionResult, 1)}))
	}

	submitResult := make(chan error, 1)
	go func() {
		submitResult <- broker.submit(context.Background(), &sessionQuestionRequest{result: make(chan sessionQuestionResult, 1)})
	}()

	started := time.Now()
	broker.close(errors.New("worker stopped"))
	assert.Less(t, time.Since(started), time.Second)
	select {
	case err := <-submitResult:
		assert.EqualError(t, err, "worker stopped")
	case <-time.After(time.Second):
		t.Fatal("blocked submit was not released by broker close")
	}
}

func TestSessionQuestionBrokerSubmitHonorsCancellationWhenQueueIsFull(t *testing.T) {
	broker := newSessionQuestionBroker()
	for i := 0; i < cap(broker.ch); i++ {
		require.NoError(t, broker.submit(context.Background(), &sessionQuestionRequest{result: make(chan sessionQuestionResult, 1)}))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := broker.submit(ctx, &sessionQuestionRequest{result: make(chan sessionQuestionResult, 1)})
	assert.ErrorIs(t, err, context.Canceled)
	broker.close(errors.New("test cleanup"))
}

func TestSessionQuestionBrokerConcurrentSubmitAndClose(t *testing.T) {
	broker := newSessionQuestionBroker()
	const submitters = 64
	results := make(chan error, submitters)
	var wg sync.WaitGroup
	for i := 0; i < submitters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- broker.submit(context.Background(), &sessionQuestionRequest{result: make(chan sessionQuestionResult, 1)})
		}()
	}
	broker.close(errors.New("concurrent shutdown"))
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			assert.EqualError(t, err, "concurrent shutdown")
		}
	}
}

func TestOrchestratorQuestionBrokerCloseTerminatesAndRejectsNewQuestions(t *testing.T) {
	broker := newOrchestratorQuestionBroker()
	assert.EqualError(t, broker.submit(context.Background(), nil), "orchestrator question request is nil")
	request := &orchestratorQuestionRequest{workerRunID: 7, question: "continue?", result: make(chan sessionQuestionResult, 1)}
	require.NoError(t, broker.submit(context.Background(), request))
	broker.close(errors.New("orchestrator ended"))
	result := <-request.result
	assert.EqualError(t, result.err, "orchestrator ended")
	_, open := broker.receive()
	assert.False(t, open)
	assert.Error(t, broker.submit(context.Background(), &orchestratorQuestionRequest{result: make(chan sessionQuestionResult, 1)}))
}

func TestOrchestratorQuestionBrokerCloseUnblocksFullQueueAndCancellation(t *testing.T) {
	broker := newOrchestratorQuestionBroker()
	for i := 0; i < cap(broker.ch); i++ {
		require.NoError(t, broker.submit(context.Background(), &orchestratorQuestionRequest{result: make(chan sessionQuestionResult, 1)}))
	}
	submitResult := make(chan error, 1)
	go func() {
		submitResult <- broker.submit(context.Background(), &orchestratorQuestionRequest{result: make(chan sessionQuestionResult, 1)})
	}()
	broker.close(errors.New("orchestrator stopped"))
	select {
	case err := <-submitResult:
		assert.EqualError(t, err, "orchestrator stopped")
	case <-time.After(time.Second):
		t.Fatal("blocked worker question was not released")
	}
	cancelBroker := newOrchestratorQuestionBroker()
	for i := 0; i < cap(cancelBroker.ch); i++ {
		require.NoError(t, cancelBroker.submit(context.Background(), &orchestratorQuestionRequest{result: make(chan sessionQuestionResult, 1)}))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assert.ErrorIs(t, cancelBroker.submit(ctx, &orchestratorQuestionRequest{result: make(chan sessionQuestionResult, 1)}), context.Canceled)
	cancelBroker.close(errors.New("test cleanup"))
	assert.EqualError(t, broker.submit(ctx, &orchestratorQuestionRequest{result: make(chan sessionQuestionResult, 1)}), "orchestrator stopped")
}

func TestOrchestratorQuestionBrokerConcurrentSubmitAndClose(t *testing.T) {
	broker := newOrchestratorQuestionBroker()
	const submitters = 64
	results := make(chan error, submitters)
	var wg sync.WaitGroup
	for i := 0; i < submitters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- broker.submit(context.Background(), &orchestratorQuestionRequest{result: make(chan sessionQuestionResult, 1)})
		}()
	}
	broker.close(errors.New("concurrent orchestrator shutdown"))
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			assert.EqualError(t, err, "concurrent orchestrator shutdown")
		}
	}
}

func TestAnswerSessionQuestionReturnsProviderErrorToWaitingOrchestrator(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	client := questionClient(func(*http.Request) (*http.Response, error) { return nil, providerErr })
	request := &sessionQuestionRequest{question: "Can you continue?", result: make(chan sessionQuestionResult, 1)}
	engine := &NativeEngine{}
	messages, err := engine.answerSessionQuestion(context.Background(), request, client, "", nil)
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
	_, err := engine.answerSessionQuestion(context.Background(), request, client, "", nil)
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
	engine.runs.sessionQuestionBrokers.Store(worker.ID, questionBroker)
	defer engine.runs.sessionQuestionBrokers.Delete(worker.ID)
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
	engine.runs.sessionQuestionBrokers.Store(worker.ID, newSessionQuestionBroker())
	defer engine.runs.sessionQuestionBrokers.Delete(worker.ID)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = engine.orchestratorAskSession(ctx, task, orchestrator.ID, worker.ID, "Will this time out?")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")
}
