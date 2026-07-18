package backup

import (
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
	if err := database.AutoMigrate(
		&db.User{}, &db.WebAuthnCredential{}, &db.Team{}, &db.TeamMember{},
		&db.Session{}, &db.PasswordResetToken{}, &db.TeamInvite{},
		&db.Company{}, &db.Project{}, &db.Sprint{}, &db.LLMProvider{},
		&db.ModelGroup{}, &db.ModelGroupMember{}, &db.DefaultModelSetting{},
		&db.Agent{}, &db.Skill{}, &db.Task{}, &db.Comment{}, &db.Attachment{},
		&db.Run{}, &db.Artifact{}, &db.ActivityLog{}, &db.ProxyRequestLog{},
		&db.MCPServer{}, &db.MCPAccount{}, &db.AgentMCPServer{},
		&db.AgentMCPAccount{}, &db.AgentMCPToolFilter{},
	); err != nil {
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
	parentTask := db.Task{CompanyID: comp.ID, ProjectID: &proj.ID, SprintID: sprint.ID, AgentID: &agentID, Title: "parent", Status: "done", TaskType: "plan_and_implement", Priority: "Normal"}
	database.Create(&parentTask)
	childTask := db.Task{CompanyID: comp.ID, SprintID: sprint.ID, ParentID: &parentTask.ID, Title: "child", Status: "backlog", TaskType: "plan_and_implement", Priority: "Normal"}
	database.Create(&childTask)
	comment := db.Comment{TaskID: parentTask.ID, AuthorType: "human", Content: "hi"}
	database.Create(&comment)
	run := db.Run{TaskID: parentTask.ID, AgentID: agent.ID, Status: "completed"}
	database.Create(&run)

	// Seed real files.
	uploads := filepath.Join(basePath, "uploads", "1")
	os.MkdirAll(uploads, 0755)
	os.WriteFile(filepath.Join(uploads, "a.txt"), []byte("upload"), 0644)
	logsDir := filepath.Join(basePath, "logs", "acme", "1", "run-1")
	os.MkdirAll(logsDir, 0755)
	os.WriteFile(filepath.Join(logsDir, "main.log"), []byte("log"), 0644)
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
		filepath.Join(newBase, "logs", "acme", "1", "run-1", "main.log"),
		filepath.Join(newBase, "ssh", "id_rsa"),
	} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("file not restored: %s", f)
		}
	}

	// Credentials must NOT be in the backup.
	if _, err := os.Stat(filepath.Join(newBase, "credentials", "secret.json")); err == nil {
		t.Error("credentials were restored — they must be excluded from backups")
	}
}

// TestBackupRestorePreservesIdentityAndSecrets verifies the multi-tenant
// alignment: the identity graph (users, teams, memberships, per-user wrapped
// keys), the tenancy columns on companies/providers, the encrypted secret
// ciphertext, and the keystore all survive a backup/restore verbatim — so a
// restored database is a faithful multi-user snapshot, not an ownerless one.
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

	// A keystore file next to the data — it must ride along so restored
	// enc: values are decryptable on a fresh machine.
	if err := os.WriteFile(filepath.Join(basePath, "keystore.json"), []byte(`{"wrapped_dek":"abc"}`), 0600); err != nil {
		t.Fatal(err)
	}

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

	// Keystore restored to the fresh base.
	if _, err := os.Stat(filepath.Join(newBase, "keystore.json")); err != nil {
		t.Errorf("keystore not restored: %v", err)
	}
}

func itoa(n int32) string {
	return strconv.Itoa(int(n))
}
