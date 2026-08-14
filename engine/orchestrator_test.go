package engine

import (
	"context"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/eventhub"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestStatusReportFreshnessWindow(t *testing.T) {
	now := time.Now()
	require.False(t, isStatusReportStale(db.RunStatusReport{ReportedAt: now.Add(-9 * time.Minute)}, true, now))
	require.True(t, isStatusReportStale(db.RunStatusReport{ReportedAt: now.Add(-10*time.Minute - time.Second)}, true, now))
	require.True(t, isStatusReportStale(db.RunStatusReport{}, false, now))
}

func TestStaleSessionStatusRequestsFreshReportOnce(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.Company{}, &db.Agent{}, &db.Sprint{}, &db.Task{}, &db.Run{}, &db.RunStatusReport{}, &db.Comment{}))
	company := db.Company{Name: "Acme", ShortName: "ACME"}
	require.NoError(t, database.Create(&company).Error)
	agent := db.Agent{CompanyID: company.ID, Name: "Worker", RoleKey: "WORKER", ShortName: "WRK"}
	require.NoError(t, database.Create(&agent).Error)
	sprint := db.Sprint{CompanyID: company.ID, Name: "Sprint"}
	require.NoError(t, database.Create(&sprint).Error)
	task := db.Task{CompanyID: company.ID, SprintID: sprint.ID, AgentID: &agent.ID, Title: "Task"}
	require.NoError(t, database.Create(&task).Error)
	orchestrator := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "running"}
	require.NoError(t, database.Create(&orchestrator).Error)
	rootID := orchestrator.ID
	require.NoError(t, database.Model(&db.Run{}).Where("id = ?", orchestrator.ID).Update("root_run_id", rootID).Error)
	worker := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "running", ParentRunID: &rootID, RootRunID: &rootID}
	require.NoError(t, database.Create(&worker).Error)

	eng := NewNativeEngine(database, eventhub.NewHub())
	ctx := context.Background()
	first, err := eng.orchestratorSessionLastRunStatus(ctx, task, orchestrator.ID, worker.ID)
	require.NoError(t, err)
	require.True(t, first.StatusReportStale)
	require.True(t, first.StatusRefreshRequested)
	second, err := eng.orchestratorSessionLastRunStatus(ctx, task, orchestrator.ID, worker.ID)
	require.NoError(t, err)
	require.True(t, second.StatusReportStale)
	require.True(t, second.StatusRefreshRequested)
	var comments []db.Comment
	require.NoError(t, database.Where("task_id = ? AND comment_type = ?", task.ID, "orchestrator_question").Find(&comments).Error)
	require.Len(t, comments, 1)
}

func ptrInt32(v int32) *int32 { return &v }
