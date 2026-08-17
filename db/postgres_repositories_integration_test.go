package db_test

// This file is intentionally reflection-backed.  Queries is a compatibility
// facade embedding every table repository; enumerating its repository methods
// at runtime means adding a new repository query cannot silently leave the
// real-PostgreSQL integration suite behind.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/db/repository"
	"agent-orchestrator/pkg/secrets"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	integrationContextType = reflect.TypeOf((*context.Context)(nil)).Elem()
	errorType              = reflect.TypeOf((*error)(nil)).Elem()
	timeType               = reflect.TypeOf(time.Time{})
)

// TestPostgresRepositoryQueries invokes every exported method on every
// repository against PostgreSQL.  Each ordinary query runs inside a rollback
// transaction, so create/update/delete methods can all be exercised against
// the same fixture without making test order part of correctness.  Migration
// helpers are run directly because DDL cannot be rolled back reliably across
// PostgreSQL versions.
func TestPostgresRepositoryQueries(t *testing.T) {
	database := openPostgres(t)
	t.Cleanup(func() { dropEverything(t, database) })
	dropEverything(t, database)
	applyPostgresMigrations(t, database)
	database.Logger = logger.Default.LogMode(logger.Silent)
	fixtures := seedRepositoryFixtures(t, database)
	resetPostgresSequences(t, database)

	base := db.New(database)
	qValue := reflect.ValueOf(base).Elem()
	type repoCase struct {
		field int
		name  string
	}
	var repos []repoCase
	for i := 0; i < qValue.NumField(); i++ {
		field := qValue.Type().Field(i)
		if !field.Anonymous || field.Type.Kind() != reflect.Pointer || !strings.HasSuffix(field.Type.Elem().Name(), "Repository") {
			continue
		}
		repos = append(repos, repoCase{field: i, name: field.Type.Elem().Name()})
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].name < repos[j].name })
	require.NotEmpty(t, repos)

	for _, repo := range repos {
		repo := repo
		t.Run(repo.name, func(t *testing.T) {
			methods := reflect.ValueOf(qValue.Field(repo.field).Interface()).Type().NumMethod()
			names := make([]string, 0, methods)
			for i := 0; i < methods; i++ {
				names = append(names, reflect.ValueOf(qValue.Field(repo.field).Interface()).Type().Method(i).Name)
			}
			sort.Strings(names)
			for callIndex, methodName := range names {
				methodName, callIndex := methodName, callIndex
				t.Run(methodName, func(t *testing.T) {
					cleanupFixture := prepareRepositoryMethodFixture(t, database, repo.name, methodName)
					defer cleanupFixture()
					migrationHelper := strings.HasPrefix(methodName, "Migrate")
					callDB := database
					if !migrationHelper {
						tx := database.Begin()
						require.NoError(t, tx.Error)
						defer tx.Rollback()
						callDB = tx
					}
					queries := db.New(callDB)
					repoValue := reflect.ValueOf(queries).Elem().Field(repo.field).Interface()
					method := reflect.ValueOf(repoValue).MethodByName(methodName)
					require.True(t, method.IsValid())
					args := make([]reflect.Value, method.Type().NumIn())
					for i := range args {
						args[i] = repositoryIntegrationArg(t, repo.name, methodName, i, method.Type().In(i), fixtures, callIndex)
					}
					var out []reflect.Value
					func() {
						defer func() {
							if recovered := recover(); recovered != nil {
								t.Fatalf("panic invoking repository query: %v", recovered)
							}
						}()
						if method.Type().IsVariadic() {
							out = method.CallSlice(args)
						} else {
							out = method.Call(args)
						}
					}()
					if err := repositoryCallError(out); err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
						t.Fatalf("real PostgreSQL query failed: %v", err)
					}
				})
			}
		})
	}
}

func repositoryCallError(out []reflect.Value) error {
	if len(out) == 0 {
		return nil
	}
	last := out[len(out)-1]
	if !last.IsValid() || !last.Type().Implements(errorType) || last.IsNil() {
		return nil
	}
	return last.Interface().(error)
}

func repositoryIntegrationArg(t *testing.T, repoName, methodName string, index int, typ reflect.Type, fixtures map[reflect.Type]any, callIndex int) reflect.Value {
	t.Helper()
	if typ.Implements(integrationContextType) {
		return reflect.ValueOf(context.Background())
	}
	if typ == timeType {
		return reflect.ValueOf(time.Now().UTC().Add(-time.Minute))
	}
	if value, ok := fixtures[typ]; ok {
		return reflect.ValueOf(repositoryFixtureArgument(value, typ, methodName, callIndex))
	}
	if typ.Kind() == reflect.Pointer {
		if typ.Elem() == timeType {
			value := time.Now().UTC().Add(time.Hour)
			return reflect.ValueOf(&value)
		}
		if typ.Elem().Kind() == reflect.Int32 {
			value := int32(1)
			return reflect.ValueOf(&value)
		}
		if value, ok := fixtures[typ.Elem()]; ok {
			copy := reflect.ValueOf(value)
			result := reflect.New(typ.Elem())
			result.Elem().Set(copy)
			return result
		}
	}
	if typ.Kind() == reflect.Slice {
		if value, ok := fixtures[typ.Elem()]; ok {
			item := reflect.ValueOf(repositoryFixtureArgument(value, typ.Elem(), methodName, callIndex))
			result := reflect.MakeSlice(typ, 1, 1)
			result.Index(0).Set(item)
			return result
		}
		switch typ.Elem().Kind() {
		case reflect.Int32:
			return reflect.ValueOf([]int32{1}).Convert(typ)
		case reflect.Int64:
			return reflect.ValueOf([]int64{1}).Convert(typ)
		case reflect.String:
			if typ.Elem() == reflect.TypeOf(db.RunEventType("")) {
				return reflect.ValueOf([]db.RunEventType{db.RunEventTypeSessionMessage}).Convert(typ)
			}
			return reflect.ValueOf([]string{"running", "paused", "done"}).Convert(typ)
		case reflect.Uint8:
			if methodName == "GetCredentialByCredentialID" {
				return reflect.ValueOf([]byte("credential-1")).Convert(typ)
			}
			return reflect.ValueOf([]byte("credential")).Convert(typ)
		}
	}
	if typ.Kind() == reflect.Map {
		if methodName == "AppendRunLogEntry" {
			return reflect.ValueOf(map[string]interface{}{"type": "integration", "message": "postgres"}).Convert(typ)
		}
		if strings.Contains(methodName, "WebhookTarget") {
			return reflect.ValueOf(map[string]any{"wake_status": "completed"}).Convert(typ)
		}
		return reflect.ValueOf(map[string]any{"status": "completed"}).Convert(typ)
	}
	switch typ.Kind() {
	case reflect.String:
		return reflect.ValueOf(repositoryStringArgument(repoName, methodName, index-1, callIndex)).Convert(typ)
	case reflect.Int32:
		if repoName == "MCPServerRepository" && methodName == "DeleteMCPServer" && index == 1 {
			return reflect.ValueOf(int32(2))
		}
		if repoName == "TeamInviteRepository" && methodName == "AcceptTeamInvite" && index == 2 {
			return reflect.ValueOf(int32(2))
		}
		return reflect.ValueOf(int32(1)).Convert(typ)
	case reflect.Int64:
		if repoName == "GitHubConnectionRepository" && methodName == "GetGitHubConnectionForAccountInstallation" {
			return reflect.ValueOf(int64(1001))
		}
		return reflect.ValueOf(int64(1)).Convert(typ)
	case reflect.Int:
		return reflect.ValueOf(1).Convert(typ)
	case reflect.Uint32:
		return reflect.ValueOf(uint32(1)).Convert(typ)
	case reflect.Bool:
		return reflect.ValueOf(true).Convert(typ)
	case reflect.Float64:
		return reflect.ValueOf(float64(1.5)).Convert(typ)
	}
	t.Fatalf("no integration argument generator for %s.%s argument %d (%s)", repoName, methodName, index, typ)
	return reflect.Zero(typ)
}

func repositoryStringArgument(repoName, methodName string, index, callIndex int) string {
	switch methodName {
	case "GetUserByEmail", "ListCompaniesForUser":
		return "fixture@example.com"
	case "GetModelGroupByKey":
		return "fixture-group"
	case "GetDefaultModelSetting", "UpdateDefaultModelSetting":
		return repository.PurposeTaskOrchestrator
	case "EnqueueRoutedEvent":
		// The routed-message API accepts a RunEventType directly rather than
		// a RunEvent struct, so it does not pass through the Create*/Save*
		// fixture normalization below.
		if index == 3 {
			return string(db.RunEventTypeSessionMessage)
		}
	case "GetArtifactByTaskAndFilename":
		return "fixture.md"
	case "GetProviderPresetByKey":
		return "fixture-preset"
	case "GetGitHubOAuthState", "ClaimGitHubOAuthState":
		return "state-1"
	case "GetTeamInviteByTokenHash":
		return "invite-token"
	case "GetSessionUser", "DeleteSessionByTokenHash":
		return "session-token"
	case "ConsumePasswordResetToken":
		return "reset-token"
	case "RevokeRefreshFamily":
		return "family-1"
	case "RotateRefreshToken":
		if index == 0 {
			return "refresh-token"
		}
		return fmt.Sprintf("rotated-token-%d", callIndex)
	case "UpdateRunLog":
		if index == 2 {
			return "running"
		}
	case "SetRunWaitState":
		return "waiting for integration"
	case "SetRunStopCause":
		return "integration"
	case "PauseRunWithMetadata":
		if index == 5 {
			return "before_tools"
		}
	case "ClaimRunForResume":
		if index == 3 {
			return "running"
		}
	case "MarkRunResumeStarted", "UpdateRunRecoveryMetadata":
		return "integration-owner"
	case "RecordResumeError":
		if index == 2 {
			return "paused"
		}
	case "MarkRunRecoverable":
		if index == 1 {
			return "recoverable_failed"
		}
	case "UpdateTaskRefinedDescription":
		return "refined integration description"
	case "CreateTeamInvite":
		switch index {
		case 1:
			return fmt.Sprintf("invitee-%d@example.com", callIndex)
		case 2:
			return "member"
		case 3:
			return fmt.Sprintf("invite-token-%d", callIndex)
		}
	case "GetGitHubAccountForUser", "GetGitHubAccountByIDForUser":
		return ""
	case "GetRunBySessionID":
		return "fixture-session"
	case "ConsumeWebAuthnSession":
		return "login"
	case "UpdateGitHubWebhookDelivery", "UpdateGitHubWebhookTarget":
		if methodName == "UpdateGitHubWebhookTarget" {
			return "attempt-1"
		}
		if index == 0 {
			return "delivery-1"
		}
		return "attempt-1"
	case "ClaimGitHubWebhookDelivery":
		if index == 0 {
			return "delivery-1"
		}
		if index == 1 {
			return "push"
		}
		return "attempt-1"
	case "ListPendingGitHubWebhookTargets", "CountPendingGitHubWebhookTargets":
		return "delivery-1"
	case "ClaimGitHubWebhookTarget":
		return "attempt-1"
	case "UpdateMCPServerToolsCache":
		return "[]"
	case "UpdateMCPServerLastError":
		return "integration error"
	case "UpdateMCPServerInitStatus":
		return "ready"
	case "UpdateLLMProviderModelCatalog", "ForceUpdateLLMProviderModelCatalog":
		return "integration"
	}
	return fmt.Sprintf("integration-%s-%d-%d", strings.ToLower(repoName), index, callIndex)
}

func repositoryFixtureArgument(value any, typ reflect.Type, methodName string, callIndex int) any {
	copy := reflect.New(typ).Elem()
	copy.Set(reflect.ValueOf(value))
	if strings.HasPrefix(methodName, "Create") || strings.HasPrefix(methodName, "Save") {
		if field := copy.FieldByName("ID"); field.IsValid() && field.CanSet() && field.Kind() >= reflect.Int && field.Kind() <= reflect.Int64 {
			field.SetInt(0)
		}
		unique := fmt.Sprintf("integration-%d", callIndex)
		for _, fieldName := range []string{"Email", "Name", "ShortName", "Slug", "Key", "TokenHash", "DeliveryID", "DedupeKey", "SessionID", "Filename", "FilePath", "Title", "Content", "CredentialID"} {
			field := copy.FieldByName(fieldName)
			if !field.IsValid() || !field.CanSet() {
				continue
			}
			switch field.Kind() {
			case reflect.String:
				field.SetString(unique)
			case reflect.Slice:
				if field.Type().Elem().Kind() == reflect.Uint8 {
					field.SetBytes([]byte(unique))
				}
			}
		}
		if field := copy.FieldByName("ID"); field.IsValid() && field.CanSet() && field.Kind() == reflect.String {
			field.SetString(fmt.Sprintf("integration-state-%d", callIndex))
		}
		if field := copy.FieldByName("Status"); field.IsValid() && field.CanSet() && field.Kind() == reflect.String {
			field.SetString("running")
		}
		if copy.Type() == reflect.TypeOf(db.Task{}) {
			if field := copy.FieldByName("Status"); field.IsValid() && field.CanSet() {
				field.SetString(db.TaskStatusTodo)
			}
		}
		if copy.Type() == reflect.TypeOf(db.Run{}) {
			if field := copy.FieldByName("Kind"); field.IsValid() && field.CanSet() {
				field.SetString(db.RunKindAgentSession)
			}
		}
		if field := copy.FieldByName("EventType"); field.IsValid() && field.CanSet() && field.Kind() == reflect.String {
			field.SetString(string(db.RunEventTypeLifecycleStatus))
		}
		if copy.Type() == reflect.TypeOf(db.TaskRelation{}) {
			if field := copy.FieldByName("Kind"); field.IsValid() && field.CanSet() {
				field.SetString(db.TaskRelationRelatedTo)
			}
		}
	}
	return copy.Interface()
}

func prepareRepositoryMethodFixture(t *testing.T, database *gorm.DB, repoName, methodName string) func() {
	t.Helper()
	if repoName == "RunEventRepository" && methodName == "AnswerPendingMessage" {
		require.NoError(t, database.Exec("UPDATE run_events SET source_run_id = 1, target_run_id = 1, event_type = 'session_message', consumed_at = NULL WHERE id = 1").Error)
		return func() {
			_ = database.Exec("UPDATE run_events SET source_run_id = NULL, target_run_id = NULL, event_type = 'run_status', consumed_at = NULL WHERE id = 1").Error
		}
	}
	if repoName == "RunRepository" && methodName == "MarkRunResumeStarted" {
		lease := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
		require.NoError(t, database.Exec("UPDATE runs SET status = 'resuming', recovery = jsonb_build_object('resume_lease_owner', 'integration-owner', 'resume_lease_until', ?::timestamptz) WHERE id = 1", lease).Error)
		return func() {
			_ = database.Exec("UPDATE runs SET status = 'running', recovery = '{}'::jsonb WHERE id = 1").Error
		}
	}
	return func() {}
}

func resetPostgresSequences(t *testing.T, database *gorm.DB) {
	t.Helper()
	sqlDB, err := database.DB()
	require.NoError(t, err)
	rows, err := sqlDB.Query(`SELECT table_name FROM information_schema.columns WHERE table_schema = 'public' AND column_name = 'id'`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var table string
		require.NoError(t, rows.Scan(&table))
		var sequence sql.NullString
		require.NoError(t, sqlDB.QueryRow(`SELECT pg_get_serial_sequence($1, 'id')`, "public."+table).Scan(&sequence))
		if !sequence.Valid {
			continue
		}
		query := fmt.Sprintf("SELECT setval($1::regclass, COALESCE((SELECT MAX(id) FROM %s), 1), true)", quotePostgresIdentifier(table))
		_, err = sqlDB.Exec(query, sequence.String)
		require.NoError(t, err)
	}
	require.NoError(t, rows.Err())
}

func quotePostgresIdentifier(identifier string) string {
	return `"public"."` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func seedRepositoryFixtures(t *testing.T, database *gorm.DB) map[reflect.Type]any {
	t.Helper()
	now := time.Now().UTC()
	dek, err := secrets.NewUserDEK()
	require.NoError(t, err)
	secrets.Default().UnlockUser(1, dek, time.Hour)
	t.Cleanup(func() { secrets.Default().LockUser(1) })
	sealed, err := secrets.Default().EncryptForUser(1, "integration-secret")
	require.NoError(t, err)

	insert := func(value any) {
		require.NoError(t, database.Create(value).Error, "%T fixture", value)
	}
	user := db.User{ID: 1, Email: "fixture@example.com", IsAdmin: true}
	insert(&user)
	secondUser := db.User{ID: 2, Email: "second@example.com"}
	insert(&secondUser)
	team := db.Team{ID: 1, Name: "fixture-team"}
	insert(&team)
	teamMember := db.TeamMember{ID: 1, TeamID: 1, UserID: 1, Role: db.TeamRoleOwner}
	insert(&teamMember)
	company := db.Company{ID: 1, Name: "Fixture Company", ShortName: "fixture", TeamID: ptrInt32(1), UserID: ptrInt32(1)}
	insert(&company)
	project := db.Project{ID: 1, CompanyID: 1, Name: "fixture-project", GitHubDefaultBranch: "main"}
	insert(&project)
	sprint := db.Sprint{ID: 1, CompanyID: 1, Name: "fixture-sprint"}
	insert(&sprint)
	provider := db.LLMProvider{ID: 1, Name: "fixture-provider", BaseUrl: "https://provider.example", ApiKeyEncrypted: sealed, UserID: ptrInt32(1), Enabled: true}
	insert(&provider)
	group := db.ModelGroup{ID: 1, Name: "Fixture Group", Slug: "fixture-group", UserID: ptrInt32(1)}
	insert(&group)
	groupMember := db.ModelGroupMember{ID: 1, GroupID: 1, ProviderID: 1, Model: "fixture-model"}
	insert(&groupMember)
	defaultSetting := db.DefaultModelSetting{ID: 1, Purpose: repository.PurposeTaskOrchestrator, UserID: ptrInt32(1), ProviderID: ptrInt32(1), Model: "fixture-model", ModelGroupID: ptrInt32(1)}
	insert(&defaultSetting)
	agent := db.Agent{ID: 1, CompanyID: 1, Name: "fixture-agent", RoleKey: "fixture-agent", SystemPrompt: "integration", ProviderID: ptrInt32(1), ModelGroupID: ptrInt32(1), Model: "fixture-model", ChatType: "message_history"}
	insert(&agent)
	skill := db.Skill{ID: 1, CompanyID: 1, Name: "fixture-skill", LocalPath: "/tmp/fixture-skill"}
	insert(&skill)
	task := db.Task{ID: 1, CompanyID: 1, ProjectID: ptrInt32(1), SprintID: 1, AgentID: ptrInt32(1), Title: "fixture-task", Priority: "Normal", Status: db.TaskStatusTodo, GitBaseBranch: "main"}
	insert(&task)
	childTask := db.Task{ID: 2, CompanyID: 1, ProjectID: ptrInt32(1), SprintID: 1, AgentID: ptrInt32(1), ParentID: ptrInt32(1), Title: "fixture-child", Priority: "Normal", Status: db.TaskStatusDone, GitBaseBranch: "main"}
	insert(&childTask)
	thirdTask := db.Task{ID: 3, CompanyID: 1, ProjectID: ptrInt32(1), SprintID: 1, AgentID: ptrInt32(1), Title: "fixture-third", Priority: "Normal", Status: db.TaskStatusTodo, GitBaseBranch: "main"}
	insert(&thirdTask)
	run := db.Run{ID: 1, TaskID: 1, AgentID: 1, Name: "fixture-run", Status: "running", SessionID: "fixture-session", StartedAt: now, LastMessageTime: ptrTime(now)}
	insert(&run)
	completedRun := db.Run{ID: 2, TaskID: 2, AgentID: 1, Name: "fixture-completed-run", Status: "completed", SessionID: "fixture-session-done", StartedAt: now.Add(-time.Hour), EndedAt: ptrTime(now.Add(-time.Minute))}
	insert(&completedRun)
	runEvent := db.RunEvent{ID: 1, TaskID: 1, RunID: 1, EventType: db.RunEventTypeLifecycleStatus, Payload: "{}", DedupeKey: "event-1", CreatedAt: now}
	insert(&runEvent)
	runStatusReport := db.RunStatusReport{ID: 1, RunID: 1, Status: "running", MessageID: 1, ReportedAt: now}
	insert(&runStatusReport)
	relation := db.TaskRelation{ID: 1, CompanyID: 1, SourceTaskID: 1, TargetTaskID: 2, Kind: db.TaskRelationDependsOn}
	insert(&relation)
	comment := db.Comment{ID: 1, TaskID: 1, AuthorType: "user", AuthorID: ptrInt32(1), Content: "fixture comment", RunID: ptrInt32(1)}
	insert(&comment)
	attachment := db.Attachment{ID: 1, TaskID: 1, CommentID: ptrInt32(1), Filename: "fixture.txt", FilePath: "/tmp/fixture.txt", MimeType: "text/plain"}
	insert(&attachment)
	artifact := db.Artifact{ID: 1, CompanyID: ptrInt32(1), ProjectID: ptrInt32(1), TaskID: 1, RunID: 1, Filename: "fixture.md", FilePath: "/tmp/fixture.md", Content: "fixture"}
	insert(&artifact)
	activity := db.ActivityLog{ID: 1, CompanyID: 1, Action: "fixture", EntityID: 1, EntityType: "task", Details: "{}"}
	insert(&activity)
	proxyLog := db.ProxyRequestLog{ID: 1, AgentID: 1, ProviderID: 1, Model: "fixture-model"}
	insert(&proxyLog)
	server := db.MCPServer{ID: 1, Name: db.MCPServerNameGitHub, DisplayName: "GitHub", Transport: db.MCPTransportBuiltin, AuthType: db.MCPAuthTypeGitHubApp, Enabled: true, Builtin: true, ProjectID: ptrInt32(1)}
	insert(&server)
	secondServer := db.MCPServer{ID: 2, Name: "fixture-server-2", DisplayName: "Fixture server", Transport: "stdio", Enabled: true}
	insert(&secondServer)
	account := db.MCPAccount{ID: 1, MCPServerID: 1, Name: "fixture-account", AuthTokenEncrypted: sealed, UserID: ptrInt32(1)}
	insert(&account)
	agentServer := db.AgentMCPServer{AgentID: 1, MCPServerID: 1, Enabled: true}
	insert(&agentServer)
	agentAccount := db.AgentMCPAccount{AgentID: 1, MCPAccountID: 1, Enabled: true}
	insert(&agentAccount)
	toolFilter := db.AgentMCPToolFilter{AgentID: 1, MCPServerID: 1, ToolName: "search", Enabled: true}
	insert(&toolFilter)
	toolStat := db.MCPToolStat{ID: 1, MCPServerID: 1, ToolName: "search", CallCount: 1}
	insert(&toolStat)
	githubConnection := db.GitHubConnection{ID: 1, InstallationID: 1001, MCPAccountID: 1, UserID: 1, AccountLogin: "fixture", ConnectedAt: now}
	insert(&githubConnection)
	identity := db.GitHubIdentity{ID: 1, MCPAccountID: 1, MCPServerID: 1, UserID: 1, GitHubUserID: 9001, GitHubLogin: "fixture"}
	insert(&identity)
	oauthState := db.GitHubOAuthState{ID: "state-1", RedirectURL: "/", MCPServerID: 1, UserID: 1, ReturnPath: "/", ExpiresAt: now.Add(time.Hour)}
	insert(&oauthState)
	delivery := db.GitHubWebhookDelivery{ID: 1, DeliveryID: "delivery-1", Event: "push", Status: "processing", AttemptToken: "attempt-1", LeaseExpiresAt: ptrTime(now.Add(-time.Hour))}
	insert(&delivery)
	target := db.GitHubWebhookTarget{ID: 1, DeliveryID: "delivery-1", TaskID: 1, CommentID: 1, WakeStatus: "pending", WakeAttemptToken: "attempt-1", WakeLeaseExpiresAt: ptrTime(now.Add(-time.Hour))}
	insert(&target)
	providerPreset := db.ProviderPreset{ID: 1, Key: "fixture-preset", Name: "Fixture", BaseUrl: "https://preset.example", ProviderType: "openai"}
	insert(&providerPreset)
	modelStat := db.ModelRequestStat{ID: 1, GroupID: ptrInt32(1), ProviderID: 1, Model: "fixture-model", Success: true, StatusCode: 200, CreatedAt: now}
	insert(&modelStat)
	session := db.Session{ID: 1, TokenHash: "session-token", UserID: 1, ExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(24 * time.Hour)}
	insert(&session)
	refresh := db.RefreshToken{ID: 1, FamilyID: "family-1", TokenHash: "refresh-token", UserID: 1, ExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(24 * time.Hour)}
	insert(&refresh)
	reset := db.PasswordResetToken{ID: 1, TokenHash: "reset-token", UserID: 1, ExpiresAt: now.Add(time.Hour)}
	insert(&reset)
	invite := db.TeamInvite{ID: 1, TeamID: 1, Email: "invitee@example.com", Role: db.TeamRoleMember, TokenHash: "invite-token", InvitedBy: 1, ExpiresAt: now.Add(time.Hour)}
	insert(&invite)
	userCredential := db.UserGitCredential{ID: 1, UserID: ptrInt32(1), SSHPrivateKeyEncrypted: sealed}
	insert(&userCredential)
	webauthnCredential := db.WebAuthnCredential{ID: 1, UserID: 1, CredentialID: []byte("credential-1"), PublicKey: []byte("public-key"), WrappedDEK: "wrapped", PRFSalt: []byte("salt"), LastUsedAt: now}
	insert(&webauthnCredential)
	webauthnSession := db.WebAuthnSession{ID: 1, UserID: ptrInt32(1), Purpose: "login", Data: "{}", ExpiresAt: now.Add(time.Hour)}
	insert(&webauthnSession)

	return map[reflect.Type]any{
		reflect.TypeOf(db.User{}):                                 user,
		reflect.TypeOf(db.Team{}):                                 team,
		reflect.TypeOf(db.TeamMember{}):                           teamMember,
		reflect.TypeOf(db.TeamInvite{}):                           invite,
		reflect.TypeOf(db.Company{}):                              company,
		reflect.TypeOf(db.Project{}):                              project,
		reflect.TypeOf(db.Sprint{}):                               sprint,
		reflect.TypeOf(db.LLMProvider{}):                          provider,
		reflect.TypeOf(db.ModelGroup{}):                           group,
		reflect.TypeOf(db.ModelGroupMember{}):                     groupMember,
		reflect.TypeOf(db.DefaultModelSetting{}):                  defaultSetting,
		reflect.TypeOf(db.Agent{}):                                agent,
		reflect.TypeOf(db.Skill{}):                                skill,
		reflect.TypeOf(db.Task{}):                                 task,
		reflect.TypeOf(db.TaskRelation{}):                         relation,
		reflect.TypeOf(db.Comment{}):                              comment,
		reflect.TypeOf(db.Attachment{}):                           attachment,
		reflect.TypeOf(db.Run{}):                                  run,
		reflect.TypeOf(db.RunEvent{}):                             runEvent,
		reflect.TypeOf(db.RunStatusReport{}):                      runStatusReport,
		reflect.TypeOf(db.Artifact{}):                             artifact,
		reflect.TypeOf(db.ActivityLog{}):                          activity,
		reflect.TypeOf(db.ProxyRequestLog{}):                      proxyLog,
		reflect.TypeOf(db.MCPServer{}):                            server,
		reflect.TypeOf(db.MCPAccount{}):                           account,
		reflect.TypeOf(db.AgentMCPServer{}):                       agentServer,
		reflect.TypeOf(db.AgentMCPAccount{}):                      agentAccount,
		reflect.TypeOf(db.AgentMCPToolFilter{}):                   toolFilter,
		reflect.TypeOf(db.MCPToolStat{}):                          toolStat,
		reflect.TypeOf(db.GitHubConnection{}):                     githubConnection,
		reflect.TypeOf(db.GitHubIdentity{}):                       identity,
		reflect.TypeOf(db.GitHubOAuthState{}):                     oauthState,
		reflect.TypeOf(db.GitHubWebhookDelivery{}):                delivery,
		reflect.TypeOf(db.GitHubWebhookTarget{}):                  target,
		reflect.TypeOf(db.ProviderPreset{}):                       providerPreset,
		reflect.TypeOf(db.ModelRequestStat{}):                     modelStat,
		reflect.TypeOf(db.Session{}):                              session,
		reflect.TypeOf(db.RefreshToken{}):                         refresh,
		reflect.TypeOf(db.PasswordResetToken{}):                   reset,
		reflect.TypeOf(db.UserGitCredential{}):                    userCredential,
		reflect.TypeOf(db.WebAuthnCredential{}):                   webauthnCredential,
		reflect.TypeOf(db.WebAuthnSession{}):                      webauthnSession,
		reflect.TypeOf(db.RunRecovery{}):                          db.RunRecovery{CheckpointPhase: db.CheckpointPhaseBeforeTools},
		reflect.TypeOf(db.RunTokenStats{}):                        db.RunTokenStats{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
		reflect.TypeOf(repository.SaveGitHubOAuthAccountParams{}): repository.SaveGitHubOAuthAccountParams{State: db.GitHubOAuthState{ID: "state-create", MCPServerID: 1, UserID: 1, ExpiresAt: now.Add(time.Hour)}, GitHubUserID: 9002, GitHubLogin: "fixture-create", SealedToken: sealed, Installations: []repository.GitHubInstallationRecord{{InstallationID: 1002, AccountLogin: "fixture"}}, ConnectedAt: now},
	}
}

func ptrInt32(value int32) *int32 { return &value }

func ptrTime(value time.Time) *time.Time { return &value }
