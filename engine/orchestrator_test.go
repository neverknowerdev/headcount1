package engine

import (
	"agent-orchestrator/db/migrations"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

func TestStatusReportFreshnessWindow(t *testing.T) {
	now := time.Now()
	require.False(t, isStatusReportStale(db.RunStatusReport{ReportedAt: now.Add(-9 * time.Minute)}, true, now))
	require.True(t, isStatusReportStale(db.RunStatusReport{ReportedAt: now.Add(-10*time.Minute - time.Second)}, true, now))
	require.True(t, isStatusReportStale(db.RunStatusReport{}, false, now))
}

func TestBuildOrchestratorSystemPromptIncludesTaskContextAndAgentRoster(t *testing.T) {
	database, err := gorm.Open(sqlite.Open("file:orchestrator-prompt-context?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	company := db.Company{Name: "Acme Labs", ShortName: "ACME", Description: "Builds privacy-first analytics for clinics."}
	require.NoError(t, database.Create(&company).Error)
	project := db.Project{CompanyID: company.ID, Name: "Care Portal", Description: "Patient-facing reporting portal", RepositoryUrl: "https://example.test/care-portal"}
	require.NoError(t, database.Create(&project).Error)
	sprint := db.Sprint{CompanyID: company.ID, Name: "Sprint 42", Goal: "Ship the audit export beta"}
	require.NoError(t, database.Create(&sprint).Error)
	worker := db.Agent{CompanyID: company.ID, Name: "Backend Builder", RoleKey: "backend", Description: "Implements APIs and database changes."}
	qa := db.Agent{CompanyID: company.ID, Name: "QA Analyst", RoleKey: "qa", Description: "Adds regression coverage and validates acceptance."}
	require.NoError(t, database.Create(&worker).Error)
	require.NoError(t, database.Create(&qa).Error)
	task := db.Task{
		CompanyID: company.ID, ProjectID: &project.ID, SprintID: sprint.ID, RefKey: "ACME-42",
		Title: "Add audit export", TaskType: "implement", Status: "to-do", Priority: "High",
		Description: "Export a patient's audit trail as CSV.", RefinedDescription: "Use the existing event ordering.",
		AcceptanceCriteria: "CSV downloads with stable headers", TestCases: "Empty audit trail; large audit trail",
		Company: company, Project: &project, Sprint: sprint,
	}
	require.NoError(t, database.Create(&task).Error)

	prompt, err := NewNativeEngine(database, eventhub.NewHub()).buildOrchestratorSystemPrompt(context.Background(), task)
	require.NoError(t, err)
	for _, expected := range []string{
		"Acme Labs", "Builds privacy-first analytics for clinics.", "Care Portal", "Patient-facing reporting portal",
		"Sprint 42", "Ship the audit export beta", "ACME-42", "Export a patient's audit trail as CSV.",
		"CSV downloads with stable headers", "Backend Builder", "Implements APIs and database changes.",
		"QA Analyst", "Adds regression coverage and validates acceptance.", "run_new_session",
	} {
		assert.Contains(t, prompt, expected)
	}
}

func TestStaleSessionStatusRequestsFreshReportOnce(t *testing.T) {
	database, err := gorm.Open(sqlite.Open("file:fork-boundary?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
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

func TestGetSessionReturnsLatestAndCompleteStatusHistory(t *testing.T) {
	database, err := gorm.Open(sqlite.Open("file:session-details?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	company := db.Company{Name: "Acme", ShortName: "ACME"}
	require.NoError(t, database.Create(&company).Error)
	agent := db.Agent{CompanyID: company.ID, Name: "Worker", RoleKey: "WORKER", ShortName: "WRK"}
	require.NoError(t, database.Create(&agent).Error)
	task := db.Task{CompanyID: company.ID, AgentID: &agent.ID, Title: "Status history"}
	require.NoError(t, database.Create(&task).Error)
	orchestrator := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "running"}
	require.NoError(t, database.Create(&orchestrator).Error)
	rootID := orchestrator.ID
	require.NoError(t, database.Model(&db.Run{}).Where("id = ?", orchestrator.ID).Update("root_run_id", rootID).Error)
	worker := db.Run{TaskID: task.ID, AgentID: agent.ID, Name: "ACME-WRK-1", Status: "running", ParentRunID: &rootID, RootRunID: &rootID}
	require.NoError(t, database.Create(&worker).Error)

	queries := db.New(database)
	require.NoError(t, queries.RecordRunStatusReport(context.Background(), worker.ID, "planning", 101))
	time.Sleep(time.Millisecond)
	require.NoError(t, queries.RecordRunStatusReport(context.Background(), worker.ID, "implementing", 202))

	engine := NewNativeEngine(database, eventhub.NewHub())
	detail, err := engine.orchestratorSessionDetails(context.Background(), task, orchestrator.ID, worker.ID)
	require.NoError(t, err)
	assert.Equal(t, worker.ID, detail.ID)
	assert.Equal(t, "running", detail.LifecycleStatus)
	require.NotNil(t, detail.LastRunStatus)
	assert.Equal(t, "implementing", detail.LastRunStatus.LastReportedStatus)
	assert.Equal(t, int64(202), detail.LastRunStatus.LastReportedMessageID)
	require.Len(t, detail.RunStatusHistory, 2)
	assert.Equal(t, "planning", detail.RunStatusHistory[0].Status)
	assert.Equal(t, int64(101), detail.RunStatusHistory[0].MessageID)
	assert.Equal(t, "implementing", detail.RunStatusHistory[1].Status)
	assert.Equal(t, int64(202), detail.RunStatusHistory[1].MessageID)

	emptyWorker := db.Run{TaskID: task.ID, AgentID: agent.ID, Name: "ACME-WRK-2", Status: "running", ParentRunID: &rootID, RootRunID: &rootID}
	require.NoError(t, database.Create(&emptyWorker).Error)
	emptyDetail, err := engine.orchestratorSessionDetails(context.Background(), task, orchestrator.ID, emptyWorker.ID)
	require.NoError(t, err)
	assert.Nil(t, emptyDetail.LastRunStatus)
	assert.Empty(t, emptyDetail.RunStatusHistory)
}

func TestGetSessionAggregatesNestedSessionStatuses(t *testing.T) {
	database, err := gorm.Open(sqlite.Open("file:nested-session-status?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	company := db.Company{Name: "Acme", ShortName: "ACME"}
	require.NoError(t, database.Create(&company).Error)
	cto := db.Agent{CompanyID: company.ID, Name: "CTO", RoleKey: "CTO", ShortName: "CTO"}
	coder := db.Agent{CompanyID: company.ID, Name: "Coder", RoleKey: "CODER", ShortName: "COD"}
	require.NoError(t, database.Create(&cto).Error)
	require.NoError(t, database.Create(&coder).Error)
	task := db.Task{CompanyID: company.ID, AgentID: &cto.ID, Title: "Nested status"}
	require.NoError(t, database.Create(&task).Error)
	orchestrator := db.Run{TaskID: task.ID, AgentID: cto.ID, Name: "ACME-orchestrator", Status: "running"}
	require.NoError(t, database.Create(&orchestrator).Error)
	rootID := orchestrator.ID
	require.NoError(t, database.Model(&db.Run{}).Where("id = ?", rootID).Update("root_run_id", rootID).Error)
	owner := db.Run{TaskID: task.ID, AgentID: cto.ID, Name: "ACME-CTO-1", Status: "running", ParentRunID: &rootID, RootRunID: &rootID}
	require.NoError(t, database.Create(&owner).Error)
	coderRun := db.Run{TaskID: task.ID, AgentID: coder.ID, Name: "ACME-COD-1", Status: "running", ParentRunID: &owner.ID, RootRunID: &rootID}
	require.NoError(t, database.Create(&coderRun).Error)

	queries := db.New(database)
	require.NoError(t, queries.RecordRunStatusReport(context.Background(), owner.ID, "waiting for Coder to finish its work", 101))
	require.NoError(t, queries.RecordRunStatusReport(context.Background(), coderRun.ID, "working on dependencies", 202))
	engine := NewNativeEngine(database, eventhub.NewHub())
	detail, err := engine.orchestratorSessionDetails(context.Background(), task, orchestrator.ID, owner.ID)
	require.NoError(t, err)
	require.NotNil(t, detail.LastRunStatus)
	assert.Equal(t, "waiting for Coder to finish its work", detail.LastRunStatus.OwnReportedStatus)
	assert.Equal(t, "waiting for Coder to finish its work. Coder status: working on dependencies", detail.LastRunStatus.LastReportedStatus)
	require.Len(t, detail.LastRunStatus.ChildStatuses, 1)
	assert.Equal(t, "Coder", detail.LastRunStatus.ChildStatuses[0].AgentName)
	assert.Equal(t, "working on dependencies", detail.LastRunStatus.ChildStatuses[0].Status)
	assert.Equal(t, int64(202), detail.LastRunStatus.ChildStatuses[0].LastReportedMessageID)
}

func TestGetSessionNestedStatusDepthIsBounded(t *testing.T) {
	database, err := gorm.Open(sqlite.Open("file:nested-status-depth?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	company := db.Company{Name: "Acme", ShortName: "ACME"}
	require.NoError(t, database.Create(&company).Error)
	agent := db.Agent{CompanyID: company.ID, Name: "Worker", RoleKey: "WORKER", ShortName: "WRK"}
	require.NoError(t, database.Create(&agent).Error)
	task := db.Task{CompanyID: company.ID, AgentID: &agent.ID, Title: "Deep status"}
	require.NoError(t, database.Create(&task).Error)
	root := db.Run{TaskID: task.ID, AgentID: agent.ID, Name: "root", Status: "running"}
	require.NoError(t, database.Create(&root).Error)
	rootID := root.ID
	require.NoError(t, database.Model(&db.Run{}).Where("id = ?", rootID).Update("root_run_id", rootID).Error)
	parentID := rootID
	var runs []db.Run
	for i := 1; i <= maxNestedStatusDepth+2; i++ {
		run := db.Run{TaskID: task.ID, AgentID: agent.ID, Name: "level-" + fmt.Sprint(i), Status: "running", ParentRunID: &parentID, RootRunID: &rootID}
		require.NoError(t, database.Create(&run).Error)
		runs = append(runs, run)
		parentID = run.ID
	}
	queries := db.New(database)
	for i, run := range runs {
		require.NoError(t, queries.RecordRunStatusReport(context.Background(), run.ID, "level-"+fmt.Sprint(i+1)+" working", int64(i+1)))
	}
	engine := NewNativeEngine(database, eventhub.NewHub())
	status, err := engine.orchestratorSessionLastRunStatus(context.Background(), task, root.ID, runs[0].ID)
	require.NoError(t, err)
	assert.Contains(t, status.LastReportedStatus, "level-6 working")
	assert.NotContains(t, status.LastReportedStatus, "level-7 working")
	assert.True(t, status.NestedStatusTruncated)
	assert.True(t, strings.Contains(status.ChildStatuses[0].Status, "level-2 working"))
}

func TestOrchestratorForkUsesNearestSafeMessageAndPreservesTree(t *testing.T) {
	database, err := gorm.Open(sqlite.Open("file:fork-boundary?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
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
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
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
