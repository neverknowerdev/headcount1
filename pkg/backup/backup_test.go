package backup

import (
	"agent-orchestrator/db/migrations"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"agent-orchestrator/db"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openTestDB(t *testing.T, dir string) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(filepath.Join(dir, "test.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	if err := migrations.ApplyGORM(database, "sqlite", "test"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return database
}

// TestBackupRestoreRoundTrip seeds a database and some real files, creates a
// backup, wipes everything, restores, and verifies entities keep their IDs
// and files reappear.
func TestBackupRestoreRoundTrip(t *testing.T) {
	basePath := t.TempDir()
	database := openTestDB(t, t.TempDir())

	// Seed entities with parent/child references.
	comp := db.Company{Name: "Acme", ShortName: "acme"}
	if err := database.Create(&comp).Error; err != nil {
		t.Fatal(err)
	}
	proj := db.Project{CompanyID: comp.ID, Name: "web", WorkspaceFolder: "acme/web"}
	database.Create(&proj)
	sprint := db.Sprint{CompanyID: comp.ID, Name: "S1"}
	database.Create(&sprint)
	agent := db.Agent{CompanyID: comp.ID, Name: "dev"}
	database.Create(&agent)
	agentID := agent.ID
	parentTask := db.Task{CompanyID: comp.ID, ProjectID: &proj.ID, SprintID: sprint.ID, AgentID: &agentID, Title: "parent", Status: db.TaskStatusDone, Priority: "Normal"}
	database.Create(&parentTask)
	childTask := db.Task{CompanyID: comp.ID, SprintID: sprint.ID, ParentID: &parentTask.ID, Title: "child", Status: db.TaskStatusBacklog, Priority: "Normal"}
	database.Create(&childTask)
	comment := db.Comment{TaskID: parentTask.ID, AuthorType: "human", Content: "hi"}
	database.Create(&comment)
	run := db.Run{TaskID: parentTask.ID, AgentID: agent.ID, Status: "completed"}
	database.Create(&run)

	// Seed real files.
	uploads := filepath.Join(basePath, "uploads", "1")
	os.MkdirAll(uploads, 0755)
	os.WriteFile(filepath.Join(uploads, "a.txt"), []byte("upload"), 0644)
	logsDir := filepath.Join(basePath, "logs", "acme", "1", "task-1")
	os.MkdirAll(logsDir, 0755)
	os.WriteFile(filepath.Join(logsDir, "run-1.jsonl"), []byte("log"), 0644)
	sshDir := filepath.Join(basePath, "ssh")
	os.MkdirAll(sshDir, 0700)
	os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("key"), 0600)
	credDir := filepath.Join(basePath, "credentials")
	os.MkdirAll(credDir, 0700)
	os.WriteFile(filepath.Join(credDir, "secret.json"), []byte("{}"), 0600)

	archivePath, err := CreateBackup(basePath, database)
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Wipe: fresh base dir, wiped DB rows.
	newBase := t.TempDir()
	for _, table := range []string{"runs", "comments", "tasks", "agents", "sprints", "projects", "companies"} {
		database.Exec("DELETE FROM " + table)
	}

	if err := RestoreBackup(archivePath, newBase, database); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	// Entities restored with original IDs.
	var gotComp db.Company
	if err := database.First(&gotComp, comp.ID).Error; err != nil {
		t.Fatalf("company %d not restored: %v", comp.ID, err)
	}
	if gotComp.ShortName != "acme" {
		t.Errorf("company short name = %q", gotComp.ShortName)
	}
	var gotChild db.Task
	if err := database.First(&gotChild, childTask.ID).Error; err != nil {
		t.Fatalf("child task not restored: %v", err)
	}
	if gotChild.ParentID == nil || *gotChild.ParentID != parentTask.ID {
		t.Errorf("child task parent = %v, want %d", gotChild.ParentID, parentTask.ID)
	}
	var gotRun db.Run
	if err := database.First(&gotRun, run.ID).Error; err != nil {
		t.Fatalf("run not restored: %v", err)
	}
	var commentCount int64
	database.Model(&db.Comment{}).Where("task_id = ?", parentTask.ID).Count(&commentCount)
	if commentCount != 1 {
		t.Errorf("comments = %d, want 1", commentCount)
	}

	// Files restored.
	for _, f := range []string{
		filepath.Join(newBase, "uploads", "1", "a.txt"),
		filepath.Join(newBase, "logs", "acme", "1", "task-1", "run-1.jsonl"),
	} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("file not restored: %s", f)
		}
	}

	// Cleartext private keys must NOT be in the backup: credentials/ and ssh/
	// hold usable secrets and are excluded (per-user keys survive as ciphertext
	// in the DB; the shared ssh key is re-provisioned on restore).
	for _, f := range []string{
		filepath.Join(newBase, "credentials", "secret.json"),
		filepath.Join(newBase, "ssh", "id_rsa"),
	} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("secret file was restored — it must be excluded from backups: %s", f)
		}
	}
}

// A restore puts the database back exactly as it was, work in flight
// included: the engine finds on its first sweep that nobody is running those
// sessions and recovers their tasks as it does after any crash. So a task's
// workflow state, its journal, its decisions and its usage all come back as
// they were, under their original ids.
func TestBackupRestoreKeepsWorkflowState(t *testing.T) {
	basePath := t.TempDir()
	database := openTestDB(t, t.TempDir())

	company := db.Company{Name: "Active Snapshot Co", ShortName: "active-snapshot"}
	if err := database.Create(&company).Error; err != nil {
		t.Fatal(err)
	}
	agent := db.Agent{CompanyID: company.ID, Name: "Coder"}
	if err := database.Create(&agent).Error; err != nil {
		t.Fatal(err)
	}
	agentID := agent.ID
	task := db.Task{CompanyID: company.ID, AgentID: &agentID, Title: "active task", Status: db.TaskStatusInProgress,
		TaskType: "coding", Mode: "direct", Phase: "execute", WaitingOn: "run"}
	if err := database.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	run := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "running"}
	if err := database.Create(&run).Error; err != nil {
		t.Fatal(err)
	}
	step := db.TaskStep{TaskID: task.ID, RootTaskID: task.ID, Kind: "run_started", RunID: &run.ID, Prompt: "brief"}
	if err := database.Create(&step).Error; err != nil {
		t.Fatal(err)
	}
	decision := db.Decision{TaskID: task.ID, RootTaskID: task.ID, RunID: &run.ID, Kind: "assumption", Title: "the API is stable"}
	if err := database.Create(&decision).Error; err != nil {
		t.Fatal(err)
	}
	call := db.LLMCall{CompanyID: company.ID, TaskID: &task.ID, Tier: "cheap", Model: "m", RunID: &run.ID, PromptTokens: 12, Status: "ok"}
	if err := database.Create(&call).Error; err != nil {
		t.Fatal(err)
	}

	archivePath, err := CreateBackup(basePath, database)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"llm_calls", "decisions", "task_steps", "runs", "tasks", "agents", "companies"} {
		database.Exec("DELETE FROM " + table)
	}
	if err := RestoreBackup(archivePath, t.TempDir(), database); err != nil {
		t.Fatal(err)
	}

	var restoredTask db.Task
	if err := database.First(&restoredTask, task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if restoredTask.Status != db.TaskStatusInProgress || restoredTask.Phase != "execute" || restoredTask.WaitingOn != "run" || restoredTask.TaskType != "coding" {
		t.Errorf("task workflow state changed by restore: %+v", restoredTask)
	}
	var restoredRun db.Run
	if err := database.First(&restoredRun, run.ID).Error; err != nil {
		t.Fatal(err)
	}
	if restoredRun.Status != "running" {
		t.Errorf("restored run status = %q, want it left running for the engine to recover", restoredRun.Status)
	}
	var restoredStep db.TaskStep
	if err := database.First(&restoredStep, step.ID).Error; err != nil || restoredStep.Prompt != "brief" || restoredStep.RunID == nil || *restoredStep.RunID != run.ID {
		t.Errorf("journal step not restored: %+v (%v)", restoredStep, err)
	}
	var restoredDecision db.Decision
	if err := database.First(&restoredDecision, decision.ID).Error; err != nil || restoredDecision.Title != "the API is stable" {
		t.Errorf("decision not restored: %+v (%v)", restoredDecision, err)
	}
	var restoredCall db.LLMCall
	if err := database.First(&restoredCall, call.ID).Error; err != nil || restoredCall.PromptTokens != 12 {
		t.Errorf("usage row not restored: %+v (%v)", restoredCall, err)
	}
}

// An archive written before the workflow engine carries columns, statuses and
// whole tables this version no longer has. What still has a place is restored;
// the rest is left behind instead of failing the row.
func TestRestoreAcceptsArchiveFromOlderVersion(t *testing.T) {
	database := openTestDB(t, t.TempDir())
	archive := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(archive, "entities", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("companies/old/company.json", `{"id": 1, "name": "Old Co", "short_name": "old"}`)
	write("companies/old/sprints/1.json", `{"id": 1, "company_id": 1, "name": "S"}`)
	write("companies/old/agents/1.json", `{"id": 1, "company_id": 1, "name": "CEO", "role_key": "CEO", "builtin": true, "enabled": true,
		"system_prompt": "legacy prompt", "model": "strong-model", "chat_type": "compact_thinking", "permissions": "{}", "can_use_workers": true}`)
	write("companies/old/tasks/1/task.json", `{"id": 1, "company_id": 1, "sprint_id": 1, "agent_id": 1, "title": "old task",
		"status": "refinement", "priority": "Normal", "orchestrator_run_id": 1}`)
	write("companies/old/tasks/1/runs/1.json", `{"id": 1, "task_id": 1, "agent_id": 1, "status": "waiting", "kind": "task_orchestrator", "root_run_id": 1}`)
	write("agent-mcp-servers.json", `[{"agent_id": 1, "mcp_server_id": 1, "enabled": true}]`)

	if err := restoreEntities(archive, database); err != nil {
		t.Fatal(err)
	}

	var agent db.Agent
	if err := database.First(&agent, 1).Error; err != nil {
		t.Fatalf("agent from an older archive was not restored: %v", err)
	}
	if agent.SystemPrompt != "legacy prompt" || !agent.Builtin {
		t.Errorf("agent restored wrong: %+v", agent)
	}
	var task db.Task
	if err := database.First(&task, 1).Error; err != nil {
		t.Fatalf("task from an older archive was not restored: %v", err)
	}
	if task.Status != db.TaskStatusBacklog || task.AgentID == nil || *task.AgentID != 1 {
		t.Errorf("task restored wrong: status=%q agent=%v", task.Status, task.AgentID)
	}
	var run db.Run
	if err := database.First(&run, 1).Error; err != nil {
		t.Fatalf("run from an older archive was not restored: %v", err)
	}
	if run.Status != "failed" || run.EndedAt == nil {
		t.Errorf("a session in a status this version does not have was restored as %q (ended_at %v)", run.Status, run.EndedAt)
	}
}

// TestBackupRestorePreservesIdentityAndSecrets verifies the multi-tenant
// alignment: the identity graph (users, teams, memberships, per-user wrapped
// keys), the tenancy columns on companies/providers, and the encrypted secret
// ciphertext all survive a backup/restore verbatim — so a restored database is
// a faithful multi-user snapshot, not an ownerless one. The passkey-wrapped DEKs
// travel in web_authn_credentials; there is no server-held keystore to archive.
func TestBackupRestorePreservesIdentityAndSecrets(t *testing.T) {
	basePath := t.TempDir()
	database := openTestDB(t, t.TempDir())

	user := db.User{Email: "owner@acme.io"}
	if err := database.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	team := db.Team{Name: "Acme Team"}
	database.Create(&team)
	database.Create(&db.TeamMember{TeamID: team.ID, UserID: user.ID, Role: db.TeamRoleOwner})
	database.Create(&db.WebAuthnCredential{
		UserID: user.ID, CredentialID: []byte("cred-abc"), PublicKey: []byte("pub"),
		WrappedDEK: "prf1:wrapped-dek", PRFSalt: []byte("salt"), Nickname: "Laptop",
	})

	comp := db.Company{Name: "Acme", ShortName: "acme", TeamID: &team.ID, UserID: &user.ID}
	database.Create(&comp)

	// A per-user provider whose api_key column holds enc:u1 ciphertext. Set
	// the raw column directly so the round-trip is tested independently of the
	// secrets subsystem (which the pkg/secrets tests cover).
	prov := db.LLMProvider{Name: "P", BaseUrl: "http://x", ApiKeyEncrypted: "", UserID: &user.ID}
	database.Create(&prov)
	sealed := "enc:u1:" + itoa(user.ID) + ":Y2lwaGVydGV4dA=="
	database.Exec("UPDATE llm_providers SET api_key = ? WHERE id = ?", sealed, prov.ID)

	archivePath, err := CreateBackup(basePath, database)
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Wipe the identity graph and tenant data, restore into a fresh base.
	newBase := t.TempDir()
	for _, table := range []string{
		"llm_providers", "companies", "web_authn_credentials", "team_members", "teams", "users",
	} {
		database.Exec("DELETE FROM " + table)
	}
	if err := RestoreBackup(archivePath, newBase, database); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	// Identity graph restored with original IDs.
	var gotUser db.User
	if err := database.First(&gotUser, user.ID).Error; err != nil {
		t.Fatalf("user not restored: %v", err)
	}
	if gotUser.Email != "owner@acme.io" {
		t.Errorf("user email = %q", gotUser.Email)
	}
	var gotCred db.WebAuthnCredential
	if err := database.First(&gotCred, "user_id = ?", user.ID).Error; err != nil {
		t.Fatalf("passkey not restored (secrets would be unrecoverable): %v", err)
	}
	if gotCred.WrappedDEK != "prf1:wrapped-dek" {
		t.Errorf("wrapped DEK = %q", gotCred.WrappedDEK)
	}
	var memberCount int64
	database.Model(&db.TeamMember{}).Where("team_id = ? AND user_id = ?", team.ID, user.ID).Count(&memberCount)
	if memberCount != 1 {
		t.Errorf("team membership not restored (count=%d)", memberCount)
	}

	// Tenancy columns preserved on the company.
	var gotComp db.Company
	database.First(&gotComp, comp.ID)
	if gotComp.TeamID == nil || *gotComp.TeamID != team.ID {
		t.Errorf("company team_id = %v, want %d", gotComp.TeamID, team.ID)
	}
	if gotComp.UserID == nil || *gotComp.UserID != user.ID {
		t.Errorf("company user_id = %v, want %d", gotComp.UserID, user.ID)
	}

	// Encrypted secret ciphertext round-tripped verbatim (raw column read).
	var gotCipher string
	database.Raw("SELECT api_key FROM llm_providers WHERE id = ?", prov.ID).Scan(&gotCipher)
	if gotCipher != sealed {
		t.Errorf("provider api_key ciphertext = %q, want %q", gotCipher, sealed)
	}
}

func itoa(n int32) string {
	return strconv.Itoa(int(n))
}
