package updater

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	dbmigrations "agent-orchestrator/db/migrations"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// UpdateRequestedExitCode is intentionally outside the normal application
// error range. A child uses it to ask the stable supervisor to stage/launch
// the candidate; the supervisor never replaces its own process image.
const UpdateRequestedExitCode = 75
const PreflightPassedExitCode = 76

var ErrUpdateRequested = errors.New("update requested by child")
var ErrPreflightPassed = errors.New("candidate preflight passed")

const (
	supervisorChildEnv     = "HEADCOUNT1_SUPERVISOR_CHILD"
	supervisorCandidateEnv = "HEADCOUNT1_SUPERVISOR_CANDIDATE"
)

// RunSupervisor owns the executable lifecycle. It starts the application as a
// child, and on the update exit code starts the journaled candidate. Keeping
// this process stable means a failed candidate cannot strand the listener or
// lose the previous executable path.
func RunSupervisor(ctx context.Context, executable string, args []string, basePath string) error {
	return RunSupervisorWithEnv(ctx, executable, args, basePath, nil)
}

// RunSupervisorWithEnv is the testable/configurable form of RunSupervisor.
// Extra environment entries are appended only to child processes.
func RunSupervisorWithEnv(ctx context.Context, executable string, args []string, basePath string, extraEnv []string) error {
	if executable == "" {
		return errors.New("supervisor executable is required")
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		// Use the resolved path for both generations so symlink replacement cannot
		// silently change the fallback target.
		executable = resolved
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	currentPath := executable
	for {
		candidate := false
		candidateSchema := ""
		if state, err := LoadState(basePath); err == nil && state.CandidatePath != "" && candidatePhase(state.Phase) && fileExists(state.CandidatePath) {
			currentPath = state.CandidatePath
			candidate = true
			candidateSchema = state.DatabaseSchema
		}
		childArgs := append([]string(nil), args...)
		cmd := exec.Command(currentPath, childArgs...)
		cmd.Env = append(append([]string(nil), os.Environ()...), extraEnv...)
		cmd.Env = append(cmd.Env, supervisorChildEnv+"=1")
		if candidate {
			cmd.Env = append(cmd.Env, supervisorCandidateEnv+"=1")
			if candidateSchema != "" {
				cmd.Env = append(cmd.Env, "HEADCOUNT1_MIGRATION_SCHEMA="+candidateSchema, "HEADCOUNT1_PREFLIGHT_ONLY=1")
			}
		}
		cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("start child %s: %w", currentPath, err)
		}
		waitErr := waitChild(ctx, cmd)
		if waitErr == nil {
			return nil
		}
		exitCode := exitCode(waitErr)
		if exitCode == PreflightPassedExitCode {
			state, err := LoadState(basePath)
			if err != nil || state.DatabaseSchema == "" {
				return fmt.Errorf("preflight passed without a shadow schema: %w", err)
			}
			if err := dropPostgresShadow(state.DatabaseSchema); err != nil {
				return fmt.Errorf("drop PostgreSQL preflight schema: %w", err)
			}
			state.DatabaseSchema = ""
			state.Phase = DeployPhaseStaged
			if err := SaveState(basePath, state); err != nil {
				return err
			}
			continue
		}
		if exitCode != UpdateRequestedExitCode {
			if candidate {
				if state, err := LoadState(basePath); err == nil && state.DatabaseSchema != "" && strings.HasPrefix(os.Getenv("DATABASE_URL"), "postgres://") {
					_ = dropPostgresShadow(state.DatabaseSchema)
				}
				// Candidate startup failures are recovered by restoring the SQLite
				// checkpoint. PostgreSQL shadow schemas are discarded by the caller's
				// journal cleanup and never become the active search path.
				if state, err := LoadState(basePath); err == nil && state.DatabaseSnapshot != "" {
					if dbPath := filepath.Join(basePath, "db", sqliteDatabaseName()); fileExists(state.DatabaseSnapshot) {
						_ = RestoreSQLiteSnapshot(state.DatabaseSnapshot, dbPath)
					}
				}
				if state, err := LoadState(basePath); err == nil && state.PreviousPath != "" && fileExists(state.PreviousPath) {
					state.Phase = DeployPhaseFailed
					state.LastError = waitErr.Error()
					_ = SaveState(basePath, state)
					currentPath = state.PreviousPath
					continue
				}
			}
			return waitErr
		}
		state, err := LoadState(basePath)
		if err != nil || state.CandidatePath == "" || !fileExists(state.CandidatePath) {
			return fmt.Errorf("update requested but candidate is unavailable: %w", err)
		}
		if os.Getenv("DATABASE_URL") == "" && state.DatabaseSnapshot == "" {
			dbPath := filepath.Join(basePath, "db", sqliteDatabaseName())
			snapshotPath := filepath.Join(basePath, "deploy", state.ID+".sqlite")
			if fileExists(dbPath) {
				snapshot, snapshotErr := CreateSQLiteSnapshot(context.Background(), dbPath, snapshotPath)
				if snapshotErr != nil {
					return fmt.Errorf("create SQLite preflight snapshot: %w", snapshotErr)
				}
				state.DatabaseSnapshot = snapshot.Path
				if err := SaveState(basePath, state); err != nil {
					return fmt.Errorf("persist SQLite preflight snapshot: %w", err)
				}
			}
		}
		if strings.HasPrefix(os.Getenv("DATABASE_URL"), "postgres://") && state.DatabaseSchema == "" {
			state.DatabaseSchema = "headcount1_deploy_" + strings.ReplaceAll(state.ID, "-", "_")
			if err := preparePostgresShadow(state.DatabaseSchema); err != nil {
				return err
			}
			if err := SaveState(basePath, state); err != nil {
				return err
			}
		}
		currentPath = state.CandidatePath
	}
}

func waitChild(ctx context.Context, cmd *exec.Cmd) error {
	result := make(chan error, 1)
	go func() { result <- cmd.Wait() }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		_ = cmd.Process.Signal(syscall.SIGTERM)
		return <-result
	}
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func sqliteDatabaseName() string {
	if os.Getenv("E2E_MODE") == "true" || os.Getenv("E2E_MODE") == "1" {
		return "headcount1-e2e.db"
	}
	return "headcount1.db"
}

func preparePostgresShadow(schemaName string) error {
	dsn := os.Getenv("DATABASE_URL")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	return dbmigrations.CreatePostgresShadowSchema(context.Background(), db, schemaName)
}

func dropPostgresShadow(schemaName string) error {
	dsn := os.Getenv("DATABASE_URL")
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	return dbmigrations.DropPostgresShadowSchema(context.Background(), db, schemaName)
}

func candidatePhase(phase DeployPhase) bool {
	return phase == DeployPhaseStaged || phase == DeployPhaseMigrating || phase == DeployPhaseStarting
}
