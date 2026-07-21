package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gorm.io/gorm"
)

// RestoreBackup restores a manifest-v2 archive: real files are copied back
// under basePath and the database is rebuilt from the entities/ JSON tree
// (with original IDs preserved). The db-snapshot/ inside the archive is a
// disaster-recovery artifact and is never read here.
func RestoreBackup(archivePath, basePath string, database *gorm.DB) error {
	log.Printf("Starting restore from %s...", archivePath)

	// Extract archive to a temp directory
	tempDir, err := os.MkdirTemp("", "headcount1-restore-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	if err := extractArchive(archivePath, tempDir); err != nil {
		return fmt.Errorf("failed to extract archive: %w", err)
	}

	if err := restoreFilesystem(tempDir, basePath); err != nil {
		log.Printf("Warning: failed to restore filesystem data: %v", err)
	}

	if err := restoreEntities(tempDir, database); err != nil {
		return fmt.Errorf("failed to rebuild database: %w", err)
	}

	repairWorktrees(basePath)

	if PostRestoreHook != nil {
		PostRestoreHook()
	}

	log.Printf("Restore completed successfully")
	return nil
}

func extractArchive(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		// Reject entries that would escape destDir (path traversal).
		target := filepath.Join(destDir, header.Name)
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("archive entry escapes destination: %s", header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

// restoreFilesystem copies the archived real-file directories (files/*)
// and settings.yaml back under basePath.
func restoreFilesystem(tempDir, basePath string) error {
	for _, dir := range fileDirs {
		src := filepath.Join(tempDir, "files", dir)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyDir(src, filepath.Join(basePath, dir)); err != nil {
			return fmt.Errorf("failed to restore %s directory: %w", dir, err)
		}
	}

	srcSettings := filepath.Join(tempDir, "settings.yaml")
	if data, err := os.ReadFile(srcSettings); err == nil {
		os.WriteFile(filepath.Join(basePath, "settings.yaml"), data, 0644)
	}

	return nil
}

// restoreEntities wipes the entity tables and re-inserts every row from the
// entities/ JSON tree, preserving original IDs so cross-references and
// ID-embedding paths (logs/{co}/{taskID}/..., uploads/{taskID}/...) stay
// valid.
func restoreEntities(tempDir string, database *gorm.DB) error {
	entitiesDir := filepath.Join(tempDir, "entities")
	if _, err := os.Stat(entitiesDir); err != nil {
		return fmt.Errorf("archive has no entities/ tree (unsupported backup version?): %w", err)
	}

	// Wipe existing data (children before parents for FK enforcement). The
	// identity graph is wiped last of all — companies (and their per-user
	// providers) reference users/teams, so they must go first; the ephemeral
	// auth tables (sessions, reset tokens, invites) are cleared too since
	// they FK to users and are not part of the archive.
	tables := []string{
		"agent_mcp_tool_filters", "agent_mcp_accounts", "agent_mcp_servers",
		"mcp_accounts", "mcp_servers",
		"run_log_entries",
		"activity_logs", "proxy_request_logs", "artifacts", "runs", "comments",
		"attachments", "tasks", "skills", "agents",
		"model_group_members", "model_groups", "default_model_settings",
		"llm_providers", "sprints", "projects", "companies",
		"team_invites", "password_reset_tokens", "sessions",
		"web_authn_sessions", "web_authn_credentials", "team_members", "teams", "users",
	}
	for _, table := range tables {
		database.Exec("DELETE FROM " + table)
	}
	database.Exec("DELETE FROM sqlite_sequence")

	byTable := map[string][]row{}
	addRows := func(table string, rows []row) {
		byTable[table] = append(byTable[table], rows...)
	}

	// Global tables.
	for _, g := range []struct{ file, table string }{
		{"users.json", "users"},
		{"teams.json", "teams"},
		{"team-members.json", "team_members"},
		{"webauthn-credentials.json", "web_authn_credentials"},
		{"llm-providers.json", "llm_providers"},
		{"model-groups.json", "model_groups"},
		{"model-group-members.json", "model_group_members"},
		{"default-model-settings.json", "default_model_settings"},
		{"mcp-servers.json", "mcp_servers"},
		{"mcp-accounts.json", "mcp_accounts"},
		{"agent-mcp-servers.json", "agent_mcp_servers"},
		{"agent-mcp-accounts.json", "agent_mcp_accounts"},
		{"agent-mcp-tool-filters.json", "agent_mcp_tool_filters"},
		{"activity-logs.json", "activity_logs"},
	} {
		rows, err := readRowsFile(filepath.Join(entitiesDir, g.file))
		if err != nil {
			log.Printf("Warning: restore: %s: %v", g.file, err)
			continue
		}
		addRows(g.table, rows)
	}

	// Company tree.
	companiesDir := filepath.Join(entitiesDir, "companies")
	companyEntries, _ := os.ReadDir(companiesDir)
	for _, ce := range companyEntries {
		if !ce.IsDir() {
			continue
		}
		compDir := filepath.Join(companiesDir, ce.Name())

		if r, err := readRowFile(filepath.Join(compDir, "company.json")); err == nil {
			addRows("companies", []row{r})
		} else {
			log.Printf("Warning: restore: company %s: %v", ce.Name(), err)
			continue
		}

		addRows("agents", readRowDir(filepath.Join(compDir, "agents")))
		addRows("sprints", readRowDir(filepath.Join(compDir, "sprints")))
		if rows, err := readRowsFile(filepath.Join(compDir, "skills.json")); err == nil {
			addRows("skills", rows)
		}

		projEntries, _ := os.ReadDir(filepath.Join(compDir, "projects"))
		for _, pe := range projEntries {
			if !pe.IsDir() {
				continue
			}
			if r, err := readRowFile(filepath.Join(compDir, "projects", pe.Name(), "project.json")); err == nil {
				addRows("projects", []row{r})
			}
		}

		taskEntries, _ := os.ReadDir(filepath.Join(compDir, "tasks"))
		for _, te := range taskEntries {
			if !te.IsDir() {
				continue
			}
			taskDir := filepath.Join(compDir, "tasks", te.Name())
			if r, err := readRowFile(filepath.Join(taskDir, "task.json")); err == nil {
				addRows("tasks", []row{r})
			} else {
				log.Printf("Warning: restore: task %s/%s: %v", ce.Name(), te.Name(), err)
				continue
			}
			if rows, err := readRowsFile(filepath.Join(taskDir, "comments.json")); err == nil {
				addRows("comments", rows)
			}
			if rows, err := readRowsFile(filepath.Join(taskDir, "attachments.json")); err == nil {
				addRows("attachments", rows)
			}
			if rows, err := readRowsFile(filepath.Join(taskDir, "artifacts.json")); err == nil {
				addRows("artifacts", rows)
			}
			addRows("runs", readRowDir(filepath.Join(taskDir, "runs")))
		}
	}

	// Insert in dependency order, each table's rows sorted by id so
	// self-references (task parent_id) resolve before their children.
	insertOrder := []string{
		"users", "teams", "team_members", "web_authn_credentials",
		"companies", "llm_providers", "model_groups", "model_group_members",
		"default_model_settings", "sprints", "projects", "agents", "skills",
		"tasks", "comments", "attachments", "artifacts", "runs",
		"mcp_servers", "mcp_accounts",
		"agent_mcp_servers", "agent_mcp_accounts", "agent_mcp_tool_filters",
		"activity_logs",
	}
	for _, table := range insertOrder {
		rows := byTable[table]
		sortRowsByID(rows)
		for _, r := range rows {
			if err := database.Table(table).Create(&r).Error; err != nil {
				log.Printf("Warning: restore: failed to insert into %s (id=%v): %v", table, r["id"], err)
			}
		}
	}

	return nil
}

func sortRowsByID(rows []row) {
	for i := 0; i < len(rows)-1; i++ {
		for j := i + 1; j < len(rows); j++ {
			if toID(rows[i]) > toID(rows[j]) {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
}

func readRowsFile(path string) ([]row, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []row
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func readRowFile(path string) (row, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r row
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, err
	}
	return r, nil
}

// readRowDir reads every {id}.json in dir; missing dirs yield no rows.
func readRowDir(dir string) []row {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var rows []row
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if r, err := readRowFile(filepath.Join(dir, e.Name())); err == nil {
			rows = append(rows, r)
		} else {
			log.Printf("Warning: restore: skipping %s: %v", filepath.Join(dir, e.Name()), err)
		}
	}
	return rows
}

// repairWorktrees runs `git worktree repair` for every restored repo,
// passing the company's worktree directories, so worktrees restored to a
// different BasePath re-link to their repos. Best-effort: failures are
// logged, never fatal.
func repairWorktrees(basePath string) {
	reposDir := filepath.Join(basePath, "repos")
	companies, err := os.ReadDir(reposDir)
	if err != nil {
		return
	}
	for _, comp := range companies {
		if !comp.IsDir() {
			continue
		}
		// Candidate worktrees for this company.
		var worktrees []string
		wsEntries, _ := os.ReadDir(filepath.Join(basePath, "workspace", comp.Name()))
		for _, w := range wsEntries {
			if w.IsDir() {
				worktrees = append(worktrees, filepath.Join(basePath, "workspace", comp.Name(), w.Name()))
			}
		}

		projects, _ := os.ReadDir(filepath.Join(reposDir, comp.Name()))
		for _, proj := range projects {
			if !proj.IsDir() {
				continue
			}
			repoDir := filepath.Join(reposDir, comp.Name(), proj.Name())
			if _, err := os.Stat(filepath.Join(repoDir, ".git")); err != nil {
				continue
			}
			args := append([]string{"-C", repoDir, "worktree", "repair"}, worktrees...)
			if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
				log.Printf("Warning: git worktree repair for %s: %v (%s)", repoDir, err, strings.TrimSpace(string(out)))
			}
		}
	}
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dstPath := filepath.Join(dst, relPath)

		if info.IsDir() {
			return os.MkdirAll(dstPath, 0755)
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		return os.WriteFile(dstPath, data, info.Mode().Perm())
	})
}

func ListBackups(basePath string) ([]string, error) {
	backupDir := filepath.Join(basePath, "backups")
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	var backups []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".tar.gz") {
			backups = append(backups, entry.Name())
		}
	}
	return backups, nil
}
