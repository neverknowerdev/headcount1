package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"
	gitpkg "agent-orchestrator/pkg/git"
)

const doneWorkspaceRetention = 10 * 24 * time.Hour

// StartWorkspaceCleanupScheduler runs one cleanup pass immediately and then
// periodically. Workspaces are intentionally retained while a task is active
// or recently completed so forks and recovery can reuse the exact filesystem
// state that produced the conversation.
func (e *NativeEngine) StartWorkspaceCleanupScheduler(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	if _, err := e.CleanupExpiredDoneWorkspaces(ctx, time.Now()); err != nil {
		fmt.Printf("Warning: workspace cleanup failed: %v\n", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if _, err := e.CleanupExpiredDoneWorkspaces(ctx, now); err != nil {
				fmt.Printf("Warning: workspace cleanup failed: %v\n", err)
			}
		}
	}
}

// CleanupExpiredDoneWorkspaces removes the task worktree and durable fork or
// helper workspaces only after the task has remained Done for the retention
// period. It returns the number of task workspace roots removed.
func (e *NativeEngine) CleanupExpiredDoneWorkspaces(ctx context.Context, now time.Time) (int, error) {
	tasks, err := e.q.ListAllTasks(ctx)
	if err != nil {
		return 0, err
	}
	cleaned := 0
	for _, task := range tasks {
		if task.ParentID != nil || !workspaceCleanupEligible(task, now, doneWorkspaceRetention) {
			continue
		}
		// Re-read immediately before deletion so a task reopened while the
		// scan was in progress is never cleaned up.
		current, err := e.q.GetTask(ctx, task.ID)
		if err != nil {
			return cleaned, err
		}
		if !workspaceCleanupEligible(current, now, doneWorkspaceRetention) {
			continue
		}
		company, err := e.q.GetCompany(ctx, current.CompanyID)
		if err != nil {
			return cleaned, err
		}
		rootTask, err := e.q.GetRootTask(ctx, current.ID)
		if err != nil {
			rootTask = current
		}
		manager := filesystem.NewManager(loadSettings().BasePath)
		paths := manager.Paths()
		rootWorkspace := manager.GetTaskWorktreePath(company, rootTask)
		if rootTask.ProjectID != nil {
			if project, projectErr := e.q.GetProject(ctx, *rootTask.ProjectID); projectErr == nil && project.RepositoryUrl != "" {
				gitManager := gitpkg.NewGitManager(manager.GetProjectRepoPath(company, project), "")
				if err := gitManager.RemoveWorktree(ctx, rootWorkspace); err != nil && !os.IsNotExist(err) {
					fmt.Printf("Warning: failed to unregister expired worktree %s: %v\n", rootWorkspace, err)
				}
			}
		}
		if err := os.RemoveAll(rootWorkspace); err != nil {
			return cleaned, fmt.Errorf("remove task workspace %s: %w", rootWorkspace, err)
		}
		if err := removeRunWorkspaces(paths.CompanyWorkspaceDir(company.ShortName), rootTask.ID); err != nil {
			return cleaned, err
		}
		cleaned++
	}
	return cleaned, nil
}

func workspaceCleanupEligible(task db.Task, now time.Time, retention time.Duration) bool {
	return task.Status == db.TaskStatusDone && task.DoneAt != nil && !task.DoneAt.After(now) && now.Sub(*task.DoneAt) >= retention
}

func removeRunWorkspaces(companyWorkspace string, taskID int32) error {
	prefix := fmt.Sprintf("session-task-%d-run-", taskID)
	entries, err := os.ReadDir(companyWorkspace)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list session workspaces: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(companyWorkspace, entry.Name())); err != nil {
			return fmt.Errorf("remove session workspace %s: %w", entry.Name(), err)
		}
	}
	return nil
}
