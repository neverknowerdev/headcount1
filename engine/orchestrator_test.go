package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestStatusReportFreshnessWindow(t *testing.T) {
	now := time.Now()
	require.False(t, isStatusReportStale(db.RunStatusReport{ReportedAt: now.Add(-9 * time.Minute)}, true, now))
	require.True(t, isStatusReportStale(db.RunStatusReport{ReportedAt: now.Add(-10*time.Minute - time.Second)}, true, now))
	require.True(t, isStatusReportStale(db.RunStatusReport{}, false, now))
}

func TestStaleSessionStatusRequestsFreshReportOnce(t *testing.T) {
	database, err := gorm.Open(sqlite.Open("file:fork-boundary?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.Company{}, &db.Agent{}, &db.Sprint{}, &db.Project{}, &db.Task{}, &db.Run{}, &db.RunStatusReport{}, &db.RunEvent{}, &db.Comment{}))
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
	var refreshes []db.RunEvent
	require.NoError(t, database.Where("run_id = ? AND event_type = ?", worker.ID, "status_report_request").Find(&refreshes).Error)
	require.Len(t, refreshes, 1)
	assert.Contains(t, refreshes[0].Payload, "report_status")
	var comments []db.Comment
	require.NoError(t, database.Where("task_id = ? AND comment_type = ?", task.ID, "orchestrator_question").Find(&comments).Error)
	require.Empty(t, comments)
}

func TestOrchestratorForkUsesNearestSafeMessageAndPreservesTree(t *testing.T) {
	database, err := gorm.Open(sqlite.Open("file:fork-boundary?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.Company{}, &db.Agent{}, &db.Sprint{}, &db.Task{}, &db.Run{}, &db.RunStatusReport{}, &db.RunEvent{}, &db.Comment{}))
	company := db.Company{Name: "Acme", ShortName: "ACME"}
	require.NoError(t, database.Create(&company).Error)
	agent := db.Agent{CompanyID: company.ID, Name: "Worker", ShortName: "WRK", RoleKey: "WORKER"}
	require.NoError(t, database.Create(&agent).Error)
	task := db.Task{CompanyID: company.ID, AgentID: &agent.ID, Title: "Fork me", RefKey: "ACME-1"}
	require.NoError(t, database.Create(&task).Error)
	orchestrator := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "running"}
	require.NoError(t, database.Create(&orchestrator).Error)
	rootID := orchestrator.ID
	require.NoError(t, database.Model(&db.Run{}).Where("id = ?", orchestrator.ID).Update("root_run_id", rootID).Error)
	logPath := filepath.Join(t.TempDir(), "source.jsonl")
	writeForkLog(t, logPath, []aicli.Message{
		{Role: "system", Content: "task"},
		{Role: "assistant", Content: "before"},
		{Role: "assistant", ToolCalls: []aicli.ToolCall{{ID: "call-1", Type: "function", Function: aicli.FuncCall{Name: "write"}}}},
		{Role: "tool", ToolCallID: "call-1", Content: "done"},
	})
	source := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "completed", ParentRunID: &rootID, RootRunID: &rootID, LogFilePath: logPath}
	require.NoError(t, database.Create(&source).Error)
	locked := source.ID
	require.NoError(t, database.Model(&db.Task{}).Where("id = ?", task.ID).Update("run_id", locked).Error)

	eng := NewNativeEngine(database, eventhub.NewHub())
	result, err := eng.orchestratorFork(context.Background(), orchestrator.ID, source.ID, 3)
	require.NoError(t, err)
	assert.Contains(t, result, "safe message 2")
	var fork db.Run
	require.Eventually(t, func() bool {
		return database.Where("task_id = ? AND id <> ?", task.ID, source.ID).Order("id desc").First(&fork).Error == nil
	}, time.Second, 10*time.Millisecond)
	assert.Equal(t, source.TaskID, fork.TaskID)
	assert.Equal(t, source.AgentID, fork.AgentID)
	assert.Equal(t, orchestrator.ID, *fork.ParentRunID)
	assert.Equal(t, orchestrator.ID, *fork.RootRunID)
	loadedTask, err := db.New(database).GetTask(context.Background(), task.ID)
	require.NoError(t, err)
	if loadedTask.RunID != nil {
		assert.NotEqual(t, source.ID, *loadedTask.RunID)
	}
}

func TestOrchestratorForkRejectsUnsafeAndOutOfTreeRequests(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, database.AutoMigrate(&db.Company{}, &db.Agent{}, &db.Project{}, &db.Task{}, &db.Run{}))
	company := db.Company{Name: "Acme", ShortName: "ACME"}
	require.NoError(t, database.Create(&company).Error)
	agent := db.Agent{CompanyID: company.ID, Name: "Worker"}
	require.NoError(t, database.Create(&agent).Error)
	task := db.Task{CompanyID: company.ID, AgentID: &agent.ID, Title: "Fork"}
	require.NoError(t, database.Create(&task).Error)
	root := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "running"}
	require.NoError(t, database.Create(&root).Error)
	require.NoError(t, database.Model(&db.Run{}).Where("id = ?", root.ID).Update("root_run_id", root.ID).Error)
	otherRoot := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "running"}
	require.NoError(t, database.Create(&otherRoot).Error)
	otherRootID := otherRoot.ID
	require.NoError(t, database.Model(&db.Run{}).Where("id = ?", otherRoot.ID).Update("root_run_id", otherRoot.ID).Error)
	outside := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "completed", RootRunID: &otherRootID}
	require.NoError(t, database.Create(&outside).Error)
	eng := NewNativeEngine(database, eventhub.NewHub())
	_, err = eng.orchestratorFork(context.Background(), root.ID, outside.ID, 1)
	assert.Error(t, err)
	_, err = eng.orchestratorFork(context.Background(), root.ID, root.ID, 1)
	assert.Error(t, err)
}

func writeForkLog(t *testing.T, path string, messages []aicli.Message) {
	t.Helper()
	file, err := os.Create(path)
	require.NoError(t, err)
	for i, message := range messages {
		encoded, err := json.Marshal(message)
		require.NoError(t, err)
		line, err := json.Marshal(map[string]interface{}{"type": "message", "seq": i + 1, "content": string(encoded)})
		require.NoError(t, err)
		_, err = file.Write(append(line, '\n'))
		require.NoError(t, err)
	}
	require.NoError(t, file.Close())
}

func ptrInt32(v int32) *int32 { return &v }
