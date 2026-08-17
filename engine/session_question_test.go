package engine

import (
	"context"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/db/migrations"
	"agent-orchestrator/eventhub"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOrchestratorAskSessionWaitsForExactDurableMessageAnswer(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, dbErr := database.DB()
	require.NoError(t, dbErr)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
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
	go func() {
		for {
			var event db.RunEvent
			if database.Where("target_run_id = ? AND event_type = ?", worker.ID, db.RunEventTypeSessionMessage).First(&event).Error == nil {
				_, _ = engine.q.AnswerPendingMessage(context.Background(), worker.ID, event.ID, "exact answer", "test-answer")
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	answer, err := engine.orchestratorSendMessage(context.Background(), task, orchestrator.ID, worker.ID, "What is the exact state?")
	require.NoError(t, err)
	assert.Equal(t, "exact answer", answer)
}

func TestOrchestratorSendMessageWaitsUntilCallerCancels(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, dbErr := database.DB()
	require.NoError(t, dbErr)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
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
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = engine.orchestratorSendMessage(ctx, task, orchestrator.ID, worker.ID, "Will this wait?")
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}
