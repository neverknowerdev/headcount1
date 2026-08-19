package backup

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"
)

// TestTenantExportImportRoundTrip is the ticket's e2e round-trip, at the engine
// level: seed a company + provider (known key) + a task with an artifact and an
// upload, export the tenant, then import it into a database that already holds
// another tenant. Assert fresh IDs, the other tenant untouched, the secret
// ciphertext relabeled to the importer (so it decrypts under their DEK), and the
// artifact/upload/log files readable at their remapped paths.
func TestTenantExportImportRoundTrip(t *testing.T) {
	srcBase := t.TempDir()
	database := openTestDB(t, t.TempDir())
	ctx := context.Background()

	// --- Source tenant (user1) in the source database. ---
	srcUser := db.User{Email: "owner@acme.io"}
	database.Create(&srcUser)
	srcTeam := db.Team{Name: "Acme"}
	database.Create(&srcTeam)
	database.Create(&db.TeamMember{TeamID: srcTeam.ID, UserID: srcUser.ID, Role: db.TeamRoleOwner})

	comp := db.Company{Name: "Acme", ShortName: "acme", TeamID: &srcTeam.ID, UserID: &srcUser.ID}
	database.Create(&comp)
	proj := db.Project{CompanyID: comp.ID, Name: "web"}
	database.Create(&proj)
	sprint := db.Sprint{CompanyID: comp.ID, Name: "S1"}
	database.Create(&sprint)
	agent := db.Agent{CompanyID: comp.ID, Name: "dev"}
	database.Create(&agent)

	prov := db.LLMProvider{Name: "P", BaseUrl: "http://x", UserID: &srcUser.ID}
	database.Create(&prov)
	// Seal a known key under the source user's id (raw column write, like the
	// existing identity test) so we can prove the label is rewritten to the
	// importer while the ciphertext body survives verbatim.
	cipherBody := "Y2lwaGVydGV4dA=="
	sealed := fmt.Sprintf("enc:u1:%d:%s", srcUser.ID, cipherBody)
	database.Exec("UPDATE llm_providers SET api_key = ? WHERE id = ?", sealed, prov.ID)

	task := db.Task{CompanyID: comp.ID, ProjectID: &proj.ID, SprintID: sprint.ID, AgentID: &agent.ID, Title: "root", Status: db.TaskStatusInProgress, Priority: "Normal"}
	database.Create(&task)
	run := db.Run{TaskID: task.ID, AgentID: agent.ID, Status: "completed"}
	database.Create(&run)
	comment := db.Comment{TaskID: task.ID, AuthorType: "human", AuthorID: &srcUser.ID, Content: "hi"}
	database.Create(&comment)

	srcPaths := filesystem.NewPaths(srcBase)

	// Upload file: uploads/{taskID}/<name>, file_path is absolute.
	uploadDir := srcPaths.TaskUploadsDir(task.ID)
	os.MkdirAll(uploadDir, 0755)
	uploadPath := filepath.Join(uploadDir, "note.txt")
	os.WriteFile(uploadPath, []byte("hello-upload"), 0644)
	database.Create(&db.Attachment{TaskID: task.ID, Filename: "note.txt", FilePath: uploadPath})

	// Artifact file: artifacts/{short}/{rootTaskID}/<name>, file_path absolute.
	artDir := srcPaths.TaskArtifactsDir("acme", task.ID)
	os.MkdirAll(artDir, 0755)
	artPath := filepath.Join(artDir, "report.md")
	os.WriteFile(artPath, []byte("hello-artifact"), 0644)
	database.Create(&db.Artifact{CompanyID: &comp.ID, ProjectID: &proj.ID, TaskID: task.ID, RunID: run.ID, Filename: "report.md", FilePath: artPath})

	// Run log: logs/{short}/{rootTaskID}/run-{rootRunID}/main.log.
	logDir := srcPaths.RunLogsDir("acme", task.ID, run.ID)
	os.MkdirAll(logDir, 0755)
	logPath := filepath.Join(logDir, "main.log")
	os.WriteFile(logPath, []byte("hello-log"), 0644)
	database.Exec("UPDATE runs SET log_file_path = ? WHERE id = ?", logPath, run.ID)

	// --- Export. ---
	var buf bytes.Buffer
	if err := ExportTenant(ctx, &buf, srcBase, database, srcUser.ID); err != nil {
		t.Fatalf("ExportTenant: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "tenant.tar.gz")
	os.WriteFile(archivePath, buf.Bytes(), 0644)

	// --- Target: a DIFFERENT database that already contains another tenant. To
	// stress ID remapping, the other tenant is seeded so that the importer's row
	// ids overlap the archive's original ids (both start from 1). ---
	targetDB := openTestDB(t, t.TempDir())

	otherUser := db.User{Email: "other@x.io"}
	targetDB.Create(&otherUser)
	otherTeam := db.Team{Name: "Other"}
	targetDB.Create(&otherTeam)
	targetDB.Create(&db.TeamMember{TeamID: otherTeam.ID, UserID: otherUser.ID, Role: db.TeamRoleOwner})
	otherComp := db.Company{Name: "Other Co", ShortName: "other", TeamID: &otherTeam.ID, UserID: &otherUser.ID}
	targetDB.Create(&otherComp)
	otherSprint := db.Sprint{CompanyID: otherComp.ID, Name: "OS"}
	targetDB.Create(&otherSprint)
	otherTask := db.Task{CompanyID: otherComp.ID, SprintID: otherSprint.ID, Title: "other task", Status: db.TaskStatusBacklog, Priority: "Normal"}
	targetDB.Create(&otherTask)

	impUser := db.User{Email: "importer@new.io"}
	targetDB.Create(&impUser)
	impTeam := db.Team{Name: "Importer"}
	targetDB.Create(&impTeam)
	targetDB.Create(&db.TeamMember{TeamID: impTeam.ID, UserID: impUser.ID, Role: db.TeamRoleOwner})

	newBase := t.TempDir()
	stats, err := ImportTenant(ctx, archivePath, newBase, targetDB, impUser.ID, impTeam.ID)
	if err != nil {
		t.Fatalf("ImportTenant: %v", err)
	}
	if stats.Companies != 1 || stats.Tasks != 1 || stats.Providers != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	// --- The other tenant is untouched. ---
	var otherStill db.Company
	if err := targetDB.First(&otherStill, otherComp.ID).Error; err != nil {
		t.Fatalf("other tenant company vanished: %v", err)
	}
	if otherStill.ShortName != "other" || *otherStill.UserID != otherUser.ID {
		t.Errorf("other tenant company mutated: %+v", otherStill)
	}
	var otherCompanyCount int64
	targetDB.Model(&db.Company{}).Where("short_name = ?", "other").Count(&otherCompanyCount)
	if otherCompanyCount != 1 {
		t.Errorf("other tenant company duplicated (count=%d)", otherCompanyCount)
	}

	// --- The imported company is a NEW row, re-owned to the importer. ---
	var imported db.Company
	if err := targetDB.Where("short_name = ? AND user_id = ?", "acme", impUser.ID).First(&imported).Error; err != nil {
		t.Fatalf("imported company not found: %v", err)
	}
	if imported.ID == comp.ID {
		t.Errorf("imported company kept the archive's id %d — must be remapped", comp.ID)
	}
	if imported.TeamID == nil || *imported.TeamID != impTeam.ID {
		t.Errorf("imported company team = %v, want %d", imported.TeamID, impTeam.ID)
	}

	// --- Provider re-owned + secret relabeled, ciphertext body preserved. ---
	var gotProv db.LLMProvider
	if err := targetDB.Where("base_url = ? AND user_id = ?", "http://x", impUser.ID).First(&gotProv).Error; err != nil {
		t.Fatalf("imported provider not found: %v", err)
	}
	var gotCipher string
	targetDB.Raw("SELECT api_key FROM llm_providers WHERE id = ?", gotProv.ID).Scan(&gotCipher)
	wantCipher := fmt.Sprintf("enc:u1:%d:%s", impUser.ID, cipherBody)
	if gotCipher != wantCipher {
		t.Errorf("provider secret label not rewritten: got %q, want %q", gotCipher, wantCipher)
	}

	// --- Task/run/artifact graph resolves with new ids. ---
	var impTask db.Task
	targetDB.Where("company_id = ?", imported.ID).First(&impTask)
	if impTask.ID == task.ID {
		t.Errorf("task id not remapped")
	}
	if impTask.ProjectID == nil || impTask.SprintID == 0 {
		t.Errorf("task FKs not remapped: %+v", impTask)
	}
	var impRun db.Run
	targetDB.Where("task_id = ?", impTask.ID).First(&impRun)
	if impRun.ID == 0 {
		t.Fatalf("run not imported")
	}

	// --- On-disk paths remapped and files readable. ---
	newPaths := filesystem.NewPaths(newBase)

	var gotAtt db.Attachment
	targetDB.Where("task_id = ?", impTask.ID).First(&gotAtt)
	wantUpload := filepath.Join(newPaths.TaskUploadsDir(impTask.ID), "note.txt")
	if gotAtt.FilePath != wantUpload {
		t.Errorf("attachment path = %q, want %q", gotAtt.FilePath, wantUpload)
	}
	assertFile(t, wantUpload, "hello-upload")

	var gotArt db.Artifact
	targetDB.Where("task_id = ?", impTask.ID).First(&gotArt)
	wantArt := filepath.Join(newPaths.TaskArtifactsDir("acme", impTask.ID), "report.md")
	if gotArt.FilePath != wantArt {
		t.Errorf("artifact path = %q, want %q", gotArt.FilePath, wantArt)
	}
	assertFile(t, wantArt, "hello-artifact")

	var gotLog string
	targetDB.Raw("SELECT log_file_path FROM runs WHERE id = ?", impRun.ID).Scan(&gotLog)
	wantLog := filepath.Join(newPaths.RunLogsDir("acme", impTask.ID, impRun.ID), "main.log")
	if gotLog != wantLog {
		t.Errorf("run log path = %q, want %q", gotLog, wantLog)
	}
	assertFile(t, wantLog, "hello-log")
}

// TestTenantImportShortNameCollision verifies that importing a company whose
// short name already exists in the target DB gets a suffixed short name (so its
// on-disk directories never collide with the existing tenant's).
func TestTenantImportShortNameCollision(t *testing.T) {
	srcBase := t.TempDir()
	database := openTestDB(t, t.TempDir())
	ctx := context.Background()

	// Existing tenant already owns short name "acme".
	existingUser := db.User{Email: "existing@x.io"}
	database.Create(&existingUser)
	existing := db.Company{Name: "Existing Acme", ShortName: "acme", UserID: &existingUser.ID}
	database.Create(&existing)

	srcUser := db.User{Email: "src@x.io"}
	database.Create(&srcUser)
	srcTeam := db.Team{Name: "Src"}
	database.Create(&srcTeam)
	database.Create(&db.TeamMember{TeamID: srcTeam.ID, UserID: srcUser.ID, Role: db.TeamRoleOwner})
	comp := db.Company{Name: "Acme", ShortName: "acme", TeamID: &srcTeam.ID, UserID: &srcUser.ID}
	database.Create(&comp)

	var buf bytes.Buffer
	if err := ExportTenant(ctx, &buf, srcBase, database, srcUser.ID); err != nil {
		t.Fatalf("ExportTenant: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "t.tar.gz")
	os.WriteFile(archivePath, buf.Bytes(), 0644)

	impUser := db.User{Email: "imp@x.io"}
	database.Create(&impUser)
	impTeam := db.Team{Name: "Imp"}
	database.Create(&impTeam)
	database.Create(&db.TeamMember{TeamID: impTeam.ID, UserID: impUser.ID, Role: db.TeamRoleOwner})

	if _, err := ImportTenant(ctx, archivePath, t.TempDir(), database, impUser.ID, impTeam.ID); err != nil {
		t.Fatalf("ImportTenant: %v", err)
	}

	var imported db.Company
	database.Where("user_id = ?", impUser.ID).First(&imported)
	if imported.ShortName == "acme" {
		t.Errorf("colliding short name not disambiguated: %q", imported.ShortName)
	}
	if !strings.HasPrefix(imported.ShortName, "acme-") {
		t.Errorf("short name = %q, want acme-<n>", imported.ShortName)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Errorf("file not readable at remapped path %s: %v", path, err)
		return
	}
	if string(data) != want {
		t.Errorf("file %s = %q, want %q", path, string(data), want)
	}
}

// TestTenantImportJoinTablesAndLinks exercises agents linked to providers/model
// groups plus MCP servers/accounts and their join tables, verifying composite-PK
// rows import and their FKs remap without error.
func TestTenantImportJoinTablesAndLinks(t *testing.T) {
	srcBase := t.TempDir()
	database := openTestDB(t, t.TempDir())
	ctx := context.Background()

	srcUser := db.User{Email: "src@j.io"}
	database.Create(&srcUser)
	srcTeam := db.Team{Name: "J"}
	database.Create(&srcTeam)
	database.Create(&db.TeamMember{TeamID: srcTeam.ID, UserID: srcUser.ID, Role: db.TeamRoleOwner})
	comp := db.Company{Name: "J Co", ShortName: "jco", TeamID: &srcTeam.ID, UserID: &srcUser.ID}
	database.Create(&comp)

	prov := db.LLMProvider{Name: "JP", BaseUrl: "http://jp", UserID: &srcUser.ID}
	database.Create(&prov)
	group := db.ModelGroup{Name: "G", Slug: "jgroup", UserID: &srcUser.ID}
	database.Create(&group)
	database.Create(&db.ModelGroupMember{GroupID: group.ID, ProviderID: prov.ID, Model: "m"})
	agent := db.Agent{CompanyID: comp.ID, Name: "a", ProviderID: &prov.ID, ModelGroupID: &group.ID}
	database.Create(&agent)

	srv := db.MCPServer{Name: "jsrv", OwnerUserID: &srcUser.ID, Transport: "http"}
	database.Create(&srv)
	acct := db.MCPAccount{MCPServerID: srv.ID, Name: "Personal", UserID: &srcUser.ID}
	database.Create(&acct)
	database.Create(&db.AgentMCPServer{AgentID: agent.ID, MCPServerID: srv.ID, Enabled: true})
	database.Create(&db.AgentMCPAccount{AgentID: agent.ID, MCPAccountID: acct.ID, Enabled: true})
	database.Create(&db.AgentMCPToolFilter{AgentID: agent.ID, MCPServerID: srv.ID, ToolName: "x", Enabled: false})

	var buf bytes.Buffer
	if err := ExportTenant(ctx, &buf, srcBase, database, srcUser.ID); err != nil {
		t.Fatalf("ExportTenant: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "j.tar.gz")
	os.WriteFile(archivePath, buf.Bytes(), 0644)

	targetDB := openTestDB(t, t.TempDir())
	impUser := db.User{Email: "imp@j.io"}
	targetDB.Create(&impUser)
	impTeam := db.Team{Name: "IJ"}
	targetDB.Create(&impTeam)
	targetDB.Create(&db.TeamMember{TeamID: impTeam.ID, UserID: impUser.ID, Role: db.TeamRoleOwner})

	if _, err := ImportTenant(ctx, archivePath, t.TempDir(), targetDB, impUser.ID, impTeam.ID); err != nil {
		t.Fatalf("ImportTenant: %v", err)
	}

	var gotAgent db.Agent
	targetDB.Where("name = ?", "a").First(&gotAgent)
	if gotAgent.ProviderID == nil || gotAgent.ModelGroupID == nil {
		t.Fatalf("agent links dropped: %+v", gotAgent)
	}
	var gotProv db.LLMProvider
	targetDB.First(&gotProv, *gotAgent.ProviderID)
	if gotProv.UserID == nil || *gotProv.UserID != impUser.ID {
		t.Errorf("agent's provider not re-owned to importer")
	}
	// Join rows reference the remapped agent.
	var amsCount, amaCount, filterCount int64
	targetDB.Model(&db.AgentMCPServer{}).Where("agent_id = ?", gotAgent.ID).Count(&amsCount)
	targetDB.Model(&db.AgentMCPAccount{}).Where("agent_id = ?", gotAgent.ID).Count(&amaCount)
	targetDB.Model(&db.AgentMCPToolFilter{}).Where("agent_id = ?", gotAgent.ID).Count(&filterCount)
	if amsCount != 1 || amaCount != 1 || filterCount != 1 {
		t.Errorf("join rows not remapped: ams=%d ama=%d filter=%d", amsCount, amaCount, filterCount)
	}
	// The MCP server got a fresh, unique name and is owned by the importer.
	var gotSrv db.MCPServer
	targetDB.Where("owner_user_id = ?", impUser.ID).First(&gotSrv)
	if gotSrv.ID == 0 {
		t.Errorf("mcp server not imported / re-owned")
	}
}

// TestTenantImportDedupNoDuplication verifies that exporting a tenant and
// importing it back into the SAME account does not create duplicate copies:
// entities are deduped by their domain key (short_name, ref_key, provider slug,
// …) and reused, while genuinely new child rows still merge in.
func TestTenantImportDedupNoDuplication(t *testing.T) {
	srcBase := t.TempDir()
	database := openTestDB(t, t.TempDir())
	ctx := context.Background()

	user := db.User{Email: "solo@dedup.io"}
	database.Create(&user)
	team := db.Team{Name: "Solo"}
	database.Create(&team)
	database.Create(&db.TeamMember{TeamID: team.ID, UserID: user.ID, Role: db.TeamRoleOwner})

	comp := db.Company{Name: "Acme", ShortName: "acme", TeamID: &team.ID, UserID: &user.ID}
	database.Create(&comp)
	proj := db.Project{CompanyID: comp.ID, Name: "web"}
	database.Create(&proj)
	sprint := db.Sprint{CompanyID: comp.ID, Name: "S1"}
	database.Create(&sprint)
	prov := db.LLMProvider{Name: "P", BaseUrl: "http://x", UserID: &user.ID}
	// Give it a slug like the app's BeforeCreate hook would.
	prov.Slug = db.ProviderSlug(prov)
	database.Create(&prov)
	task := db.Task{CompanyID: comp.ID, ProjectID: &proj.ID, SprintID: sprint.ID, Title: "root", RefKey: "ACME-1", Status: db.TaskStatusBacklog, Priority: "Normal"}
	database.Create(&task)

	countAll := func() (companies, projects, tasks, providers, sprints int64) {
		database.Model(&db.Company{}).Count(&companies)
		database.Model(&db.Project{}).Count(&projects)
		database.Model(&db.Task{}).Count(&tasks)
		database.Model(&db.LLMProvider{}).Count(&providers)
		database.Model(&db.Sprint{}).Count(&sprints)
		return
	}
	c0, p0, t0, pr0, s0 := countAll()

	exportAndImport := func() TenantImportStats {
		var buf bytes.Buffer
		if err := ExportTenant(ctx, &buf, srcBase, database, user.ID); err != nil {
			t.Fatalf("ExportTenant: %v", err)
		}
		archivePath := filepath.Join(t.TempDir(), "t.tar.gz")
		os.WriteFile(archivePath, buf.Bytes(), 0644)
		stats, err := ImportTenant(ctx, archivePath, srcBase, database, user.ID, team.ID)
		if err != nil {
			t.Fatalf("ImportTenant: %v", err)
		}
		return stats
	}

	// First import: everything already exists (we exported our own live data),
	// so nothing new should be created.
	stats := exportAndImport()
	if stats.Companies != 0 || stats.Projects != 0 || stats.Tasks != 0 || stats.Providers != 0 || stats.Sprints != 0 {
		t.Errorf("re-import created new rows, expected all deduped: %+v", stats)
	}
	c1, p1, t1, pr1, s1 := countAll()
	if c1 != c0 || p1 != p0 || t1 != t0 || pr1 != pr0 || s1 != s0 {
		t.Errorf("row counts changed after re-import: companies %d->%d, projects %d->%d, tasks %d->%d, providers %d->%d, sprints %d->%d",
			c0, c1, p0, p1, t0, t1, pr0, pr1, s0, s1)
	}

	// A second import is likewise a no-op — proving idempotency.
	exportAndImport()
	c2, p2, t2, pr2, s2 := countAll()
	if c2 != c0 || p2 != p0 || t2 != t0 || pr2 != pr0 || s2 != s0 {
		t.Errorf("second re-import duplicated rows: companies=%d projects=%d tasks=%d providers=%d sprints=%d", c2, p2, t2, pr2, s2)
	}

	// Now add a genuinely new task, re-import, and confirm ONLY it is added
	// (merge of new children onto the existing, deduped subtree).
	newTask := db.Task{CompanyID: comp.ID, SprintID: sprint.ID, Title: "second", RefKey: "ACME-2", Status: db.TaskStatusBacklog, Priority: "Normal"}
	database.Create(&newTask)
	stats = exportAndImport()
	if stats.Tasks != 0 {
		// The new task already lives in this DB (we just created it), so exporting
		// then importing into the same DB still finds it by ref_key → deduped.
		t.Errorf("expected the new task to dedup on re-import, got %+v", stats)
	}
	var taskCount int64
	database.Model(&db.Task{}).Where("company_id = ?", comp.ID).Count(&taskCount)
	if taskCount != 2 {
		t.Errorf("task count = %d, want 2 (no duplication)", taskCount)
	}
}

// TestTenantImportDedupLegacyTaskWithoutRefKey verifies that archives made from
// legacy/directly-created tasks remain idempotent even when ref_key is empty.
// The fallback must include creation metadata so two same-title tasks are not
// incorrectly merged.
func TestTenantImportDedupLegacyTaskWithoutRefKey(t *testing.T) {
	basePath := t.TempDir()
	database := openTestDB(t, t.TempDir())
	ctx := context.Background()
	must := func(err error) {
		if err != nil {
			t.Fatalf("database setup: %v", err)
		}
	}

	user := db.User{Email: "legacy-dedup@dedup.io"}
	must(database.Create(&user).Error)
	team := db.Team{Name: "Legacy dedup"}
	must(database.Create(&team).Error)
	must(database.Create(&db.TeamMember{TeamID: team.ID, UserID: user.ID, Role: db.TeamRoleOwner}).Error)
	company := db.Company{Name: "Legacy", ShortName: "legacy", TeamID: &team.ID, UserID: &user.ID}
	must(database.Create(&company).Error)
	sprint := db.Sprint{CompanyID: company.ID, Name: "Legacy sprint"}
	must(database.Create(&sprint).Error)

	createdAt := time.Date(2024, 1, 2, 3, 4, 5, 678000000, time.UTC)
	legacy := db.Task{
		CompanyID: company.ID,
		SprintID:  sprint.ID,
		Title:     "same title",
		Status:    db.TaskStatusBacklog,
		Priority:  "Normal",
		CreatedAt: createdAt,
		UpdatedAt: createdAt,
	}
	must(database.Create(&legacy).Error)

	var archive bytes.Buffer
	if err := ExportTenant(ctx, &archive, basePath, database, user.ID); err != nil {
		t.Fatalf("ExportTenant: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "legacy.tar.gz")
	if err := os.WriteFile(archivePath, archive.Bytes(), 0644); err != nil {
		t.Fatalf("write archive: %v", err)
	}

	stats, err := ImportTenant(ctx, archivePath, basePath, database, user.ID, team.ID)
	if err != nil {
		t.Fatalf("ImportTenant: %v", err)
	}
	if stats.Tasks != 0 {
		t.Fatalf("legacy task was duplicated: %+v", stats)
	}

	var count int64
	database.Model(&db.Task{}).Where("company_id = ?", company.ID).Count(&count)
	if count != 1 {
		t.Fatalf("task count = %d, want 1", count)
	}

	// A same-title task with a different creation timestamp is a separate task.
	second := legacy
	second.ID = 0
	second.CreatedAt = createdAt.Add(time.Minute)
	second.UpdatedAt = second.CreatedAt
	must(database.Create(&second).Error)
	stats, err = ImportTenant(ctx, archivePath, basePath, database, user.ID, team.ID)
	if err != nil {
		t.Fatalf("ImportTenant after second task: %v", err)
	}
	if stats.Tasks != 0 {
		t.Fatalf("same-title task was unexpectedly imported: %+v", stats)
	}
	database.Model(&db.Task{}).Where("company_id = ?", company.ID).Count(&count)
	if count != 2 {
		t.Fatalf("task count after distinct same-title task = %d, want 2", count)
	}
}

// TestTenantImportDedupTaskWithMissingTargetRefKey verifies that a newer
// archive still deduplicates against a legacy target row whose ref_key is
// empty. The exact key is preferred, while the title/creation-time identity is
// the compatibility fallback when the target cannot match it.
func TestTenantImportDedupTaskWithMissingTargetRefKey(t *testing.T) {
	basePath := t.TempDir()
	database := openTestDB(t, t.TempDir())
	ctx := context.Background()
	must := func(err error) {
		if err != nil {
			t.Fatalf("database setup: %v", err)
		}
	}

	user := db.User{Email: "mixed-version@dedup.io"}
	must(database.Create(&user).Error)
	team := db.Team{Name: "Mixed version"}
	must(database.Create(&team).Error)
	must(database.Create(&db.TeamMember{TeamID: team.ID, UserID: user.ID, Role: db.TeamRoleOwner}).Error)
	company := db.Company{Name: "Mixed", ShortName: "mixed", TeamID: &team.ID, UserID: &user.ID}
	must(database.Create(&company).Error)
	sprint := db.Sprint{CompanyID: company.ID, Name: "Sprint"}
	must(database.Create(&sprint).Error)
	createdAt := time.Date(2024, 2, 3, 4, 5, 6, 789123456, time.UTC)
	task := db.Task{CompanyID: company.ID, SprintID: sprint.ID, Title: "same task", RefKey: "MIXED-1", Status: db.TaskStatusBacklog, Priority: "Normal", CreatedAt: createdAt, UpdatedAt: createdAt}
	must(database.Create(&task).Error)

	var archive bytes.Buffer
	if err := ExportTenant(ctx, &archive, basePath, database, user.ID); err != nil {
		t.Fatalf("ExportTenant: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "mixed-version.tar.gz")
	must(os.WriteFile(archivePath, archive.Bytes(), 0644))

	// Simulate a target created before TaskRepository started generating ref_key.
	must(database.Model(&db.Task{}).Where("id = ?", task.ID).Updates(map[string]interface{}{
		"ref_key": "",
		// Simulate PostgreSQL's microsecond timestamp precision in a legacy
		// target while the archive retains nanoseconds.
		"created_at": createdAt.Truncate(time.Microsecond),
	}).Error)
	stats, err := ImportTenant(ctx, archivePath, basePath, database, user.ID, team.ID)
	if err != nil {
		t.Fatalf("ImportTenant: %v", err)
	}
	if stats.Tasks != 0 {
		t.Fatalf("task with a missing target ref_key was duplicated: %+v", stats)
	}
	var count int64
	database.Model(&db.Task{}).Where("company_id = ?", company.ID).Count(&count)
	if count != 1 {
		t.Fatalf("task count = %d, want 1", count)
	}
}

// TestTenantImportMergeChildren verifies "merge children only": when a company
// already exists in the target (matched by short_name), it is reused rather than
// duplicated, and only the tasks/runs the target lacks are added.
func TestTenantImportMergeChildren(t *testing.T) {
	srcBase := t.TempDir()
	srcDB := openTestDB(t, t.TempDir())
	ctx := context.Background()

	// Source: company "acme" with two tasks.
	sUser := db.User{Email: "src@merge.io"}
	srcDB.Create(&sUser)
	sTeam := db.Team{Name: "S"}
	srcDB.Create(&sTeam)
	srcDB.Create(&db.TeamMember{TeamID: sTeam.ID, UserID: sUser.ID, Role: db.TeamRoleOwner})
	sComp := db.Company{Name: "Acme", ShortName: "acme", TeamID: &sTeam.ID, UserID: &sUser.ID}
	srcDB.Create(&sComp)
	sSprint := db.Sprint{CompanyID: sComp.ID, Name: "S1"}
	srcDB.Create(&sSprint)
	srcDB.Create(&db.Task{CompanyID: sComp.ID, SprintID: sSprint.ID, Title: "shared", RefKey: "ACME-1", Status: db.TaskStatusBacklog, Priority: "Normal"})
	srcDB.Create(&db.Task{CompanyID: sComp.ID, SprintID: sSprint.ID, Title: "only-in-source", RefKey: "ACME-2", Status: db.TaskStatusBacklog, Priority: "Normal"})

	var buf bytes.Buffer
	if err := ExportTenant(ctx, &buf, srcBase, srcDB, sUser.ID); err != nil {
		t.Fatalf("ExportTenant: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "m.tar.gz")
	os.WriteFile(archivePath, buf.Bytes(), 0644)

	// Target: a DIFFERENT db + account that ALREADY has an "acme" company with
	// only ACME-1. Import should reuse that company and add only ACME-2.
	tgtDB := openTestDB(t, t.TempDir())
	tUser := db.User{Email: "tgt@merge.io"}
	tgtDB.Create(&tUser)
	tTeam := db.Team{Name: "T"}
	tgtDB.Create(&tTeam)
	tgtDB.Create(&db.TeamMember{TeamID: tTeam.ID, UserID: tUser.ID, Role: db.TeamRoleOwner})
	tComp := db.Company{Name: "Acme", ShortName: "acme", TeamID: &tTeam.ID, UserID: &tUser.ID}
	tgtDB.Create(&tComp)
	tSprint := db.Sprint{CompanyID: tComp.ID, Name: "S1"}
	tgtDB.Create(&tSprint)
	tgtDB.Create(&db.Task{CompanyID: tComp.ID, SprintID: tSprint.ID, Title: "shared (target copy)", RefKey: "ACME-1", Status: db.TaskStatusDone, Priority: "Normal"})

	stats, err := ImportTenant(ctx, archivePath, t.TempDir(), tgtDB, tUser.ID, tTeam.ID)
	if err != nil {
		t.Fatalf("ImportTenant: %v", err)
	}
	if stats.Companies != 0 {
		t.Errorf("existing company should be reused, not re-created: %+v", stats)
	}
	if stats.Tasks != 1 {
		t.Errorf("expected exactly 1 new task merged in, got %d", stats.Tasks)
	}

	// Still exactly ONE acme company, owned by the target user.
	var acmeCount int64
	tgtDB.Model(&db.Company{}).Where("short_name = ?", "acme").Count(&acmeCount)
	if acmeCount != 1 {
		t.Errorf("acme company count = %d, want 1 (reused, not duplicated)", acmeCount)
	}

	// Both ACME-1 (pre-existing, untouched) and ACME-2 (merged) present, no dup.
	var refKeys []string
	tgtDB.Model(&db.Task{}).Where("company_id = ?", tComp.ID).Order("ref_key").Pluck("ref_key", &refKeys)
	if len(refKeys) != 2 || refKeys[0] != "ACME-1" || refKeys[1] != "ACME-2" {
		t.Errorf("task ref_keys = %v, want [ACME-1 ACME-2]", refKeys)
	}

	// The pre-existing ACME-1 kept its own title/status (not overwritten).
	var shared db.Task
	tgtDB.Where("company_id = ? AND ref_key = ?", tComp.ID, "ACME-1").First(&shared)
	if shared.Status != "done" || shared.Title != "shared (target copy)" {
		t.Errorf("existing task was clobbered by merge: %+v", shared)
	}
}
