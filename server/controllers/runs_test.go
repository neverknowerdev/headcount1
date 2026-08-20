package endpoints

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"

	"github.com/stretchr/testify/require"
)

func TestDownloadRunLogsArchivesNestedSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("E2E_HEADCOUNT1_HOME", home)
	base := filepath.Join(home, "data")
	require.NoError(t, os.MkdirAll(base, 0o755))
	require.NoError(t, SaveSettings(Settings{BasePath: base}))

	paths := filesystem.NewPaths(base)
	logDir := paths.RunLogsDir("HC1", 7, 42)
	require.NoError(t, os.MkdirAll(filepath.Join(logDir, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(logDir, "root.jsonl"), []byte("root log\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(logDir, "nested", "child.jsonl"), []byte("child log\n"), 0o644))

	run := db.Run{
		ID:     42,
		TaskID: 7,
		Task:   db.Task{Company: db.Company{ShortName: "HC1"}},
	}
	api := &API{}
	req := httptest.NewRequest(http.MethodGet, "/api/runs/42/download", nil)
	req = req.WithContext(context.WithValue(req.Context(), runKey, run))
	res := httptest.NewRecorder()

	api.DownloadRunLogs(res, req)

	require.Equal(t, http.StatusOK, res.Code)
	require.Equal(t, "application/zip", res.Header().Get("Content-Type"))
	require.Contains(t, res.Header().Get("Content-Disposition"), `run-42-logs.zip`)

	archiveReader, err := zip.NewReader(bytes.NewReader(res.Body.Bytes()), int64(res.Body.Len()))
	require.NoError(t, err)
	contents := make(map[string]string, len(archiveReader.File))
	for _, file := range archiveReader.File {
		reader, openErr := file.Open()
		require.NoError(t, openErr)
		data, readErr := io.ReadAll(reader)
		require.NoError(t, readErr)
		require.NoError(t, reader.Close())
		contents[file.Name] = string(data)
	}

	require.Equal(t, "root log\n", contents["root.jsonl"])
	require.Equal(t, "child log\n", contents["nested/child.jsonl"])
}

func TestToRunResponseUsesControlPlaneIdentityForOrchestrator(t *testing.T) {
	ceo := db.Agent{Name: "CEO Agent"}

	orchestrator := toRunResponse(db.Run{Kind: db.RunKindTaskOrchestrator, Agent: ceo})
	require.Equal(t, "Orchestrator", orchestrator.AgentName)

	worker := toRunResponse(db.Run{Kind: db.RunKindAgentSession, Agent: ceo})
	require.Equal(t, "CEO Agent", worker.AgentName)
	helper := toRunResponse(db.Run{Kind: db.RunKindHelperWorker, Agent: ceo, Title: "Verify repository state"})
	require.Equal(t, "Worker", helper.AgentName)
	require.Equal(t, "Verify repository state", helper.Title)
}
