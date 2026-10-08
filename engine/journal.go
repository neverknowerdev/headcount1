package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"
)

// Names of the files in a task's log folder.
const (
	journalFileName   = "task.jsonl"
	decisionsFileName = "decisions.jsonl"
)

// taskLogDir is where a task's own logs live on disk.
func taskLogDir(basePath, companyShortName string, task db.Task) string {
	return filesystem.NewPaths(basePath).TaskJournalDir(companyShortName, task.RootTaskID, task.ID)
}

// appendJSONLines appends one JSON document per line to a file, creating the
// folder and file as needed.
func appendJSONLines(path string, values ...interface{}) error {
	if len(values) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			return err
		}
	}
	return nil
}

// mirrorJournal copies committed journal steps and decisions to the task's
// log folder. The database is the record; the files are a readable copy that
// travels with the task's other logs, so a failure here is only logged.
func mirrorJournal(basePath, companyShortName string, task db.Task, steps []db.TaskStep, decisions []db.Decision) {
	if basePath == "" || companyShortName == "" {
		return
	}
	dir := taskLogDir(basePath, companyShortName, task)
	stepValues := make([]interface{}, len(steps))
	for i := range steps {
		stepValues[i] = steps[i]
	}
	if err := appendJSONLines(filepath.Join(dir, journalFileName), stepValues...); err != nil {
		fmt.Printf("Warning: failed to write journal of task %d: %v\n", task.ID, err)
	}
	decisionValues := make([]interface{}, len(decisions))
	for i := range decisions {
		decisionValues[i] = decisions[i]
	}
	if err := appendJSONLines(filepath.Join(dir, decisionsFileName), decisionValues...); err != nil {
		fmt.Printf("Warning: failed to write decisions of task %d: %v\n", task.ID, err)
	}
}
