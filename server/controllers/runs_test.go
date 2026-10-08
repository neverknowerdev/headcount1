package endpoints

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/db/migrations"
	"agent-orchestrator/pkg/filesystem"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A top-level task's log archive holds the journal, decisions and executor
// sessions of every task in its tree; a subtask's holds only its own.
func TestDownloadTaskLogsArchivesTheTaskTree(t *testing.T) {
	home := t.TempDir()
	t.Setenv("E2E_HEADCOUNT1_HOME", home)
	base := filepath.Join(home, "data")
	require.NoError(t, os.MkdirAll(base, 0o755))
	require.NoError(t, SaveSettings(Settings{BasePath: base}))

	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	company := db.Company{Name: "HeadCount1", ShortName: "HC1"}
	require.NoError(t, database.Create(&company).Error)
	q := db.New(database)
	root, err := q.CreateTask(context.Background(), db.Task{CompanyID: company.ID, Title: "root"})
	require.NoError(t, err)
	child, err := q.CreateTask(context.Background(), db.Task{CompanyID: company.ID, Title: "child", ParentID: &root.ID})
	require.NoError(t, err)

	paths := filesystem.NewPaths(base)
	rootDir := paths.TaskJournalDir("HC1", root.ID, root.ID)
	childDir := paths.TaskJournalDir("HC1", root.ID, child.ID)
	require.NoError(t, os.MkdirAll(rootDir, 0o755))
	require.NoError(t, os.MkdirAll(childDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(rootDir, "task.jsonl"), []byte("root journal\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(childDir, "run-5.jsonl"), []byte("child session\n"), 0o644))

	api := NewAPI(database, nil, nil)
	download := func(task db.Task) map[string]string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/tasks/1/logs/download", nil)
		req = req.WithContext(context.WithValue(req.Context(), taskKey, task))
		res := httptest.NewRecorder()
		api.DownloadTaskLogs(res, req)
		require.Equal(t, http.StatusOK, res.Code, res.Body.String())
		require.Equal(t, "application/zip", res.Header().Get("Content-Type"))
		require.Contains(t, res.Header().Get("Content-Disposition"), task.RefKey+"-logs.zip")
		archive, err := zip.NewReader(bytes.NewReader(res.Body.Bytes()), int64(res.Body.Len()))
		require.NoError(t, err)
		contents := make(map[string]string, len(archive.File))
		for _, file := range archive.File {
			reader, openErr := file.Open()
			require.NoError(t, openErr)
			data, readErr := io.ReadAll(reader)
			require.NoError(t, readErr)
			require.NoError(t, reader.Close())
			contents[file.Name] = string(data)
		}
		return contents
	}

	require.Equal(t, map[string]string{
		fmt.Sprintf("task-%d/task.jsonl", root.ID):   "root journal\n",
		fmt.Sprintf("task-%d/run-5.jsonl", child.ID): "child session\n",
	}, download(root))
	require.Equal(t, map[string]string{"run-5.jsonl": "child session\n"}, download(child))

	// A task that never ran has nothing to download.
	idle, err := q.CreateTask(context.Background(), db.Task{CompanyID: company.ID, Title: "idle"})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "/api/tasks/3/logs/download", nil)
	req = req.WithContext(context.WithValue(req.Context(), taskKey, idle))
	res := httptest.NewRecorder()
	api.DownloadTaskLogs(res, req)
	require.Equal(t, http.StatusNotFound, res.Code)
}

func TestToRunResponseNamesTheRoleTheSessionRanAs(t *testing.T) {
	run := toRunResponse(db.Run{Agent: db.Agent{Name: "Coder"}, Title: "Verify repository state"})
	require.Equal(t, "Coder", run.AgentName)
	require.Equal(t, "Verify repository state", run.Title)
}
