package backup

// Postgres-specific restore test. Skipped unless TEST_POSTGRES_URL is set.
//
//	TEST_POSTGRES_URL="postgres://postgres@127.0.0.1:5433/orchestrator?sslmode=disable" \
//	    go test ./pkg/backup/ -run TestPostgres -v
//
// Restore inserts rows with explicit ids. On Postgres that does NOT advance the
// tables' serial sequences, so without an explicit realignment the next auto-id
// insert collides with a restored row. This test reproduces exactly that path.

import (
	"context"
	"os"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/db/migrations"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func openPostgresTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_URL not set; skipping Postgres restore test")
	}
	database, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if err := database.Exec("DROP SCHEMA public CASCADE").Error; err != nil {
		t.Fatalf("drop schema: %v", err)
	}
	if err := database.Exec("CREATE SCHEMA public").Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("database handle: %v", err)
	}
	if err := migrations.Apply(context.Background(), sqlDB, "postgres", "integration-test"); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return database
}

func TestPostgresRestoreRealignsSequences(t *testing.T) {
	basePath := t.TempDir()
	database := openPostgresTestDB(t)
	t.Cleanup(func() {
		if err := database.Exec("DROP SCHEMA public CASCADE").Error; err != nil {
			t.Errorf("cleanup postgres schema: %v", err)
			return
		}
		if err := database.Exec("CREATE SCHEMA public").Error; err != nil {
			t.Errorf("recreate postgres schema: %v", err)
		}
	})

	// Seed a small tree with non-1 ids so the sequence must advance past them.
	comp := db.Company{Name: "Acme", ShortName: "acme"}
	if err := database.Create(&comp).Error; err != nil {
		t.Fatal(err)
	}
	proj := db.Project{CompanyID: comp.ID, Name: "web"}
	database.Create(&proj)
	sprint := db.Sprint{CompanyID: comp.ID, Name: "S1"}
	database.Create(&sprint)
	agent := db.Agent{CompanyID: comp.ID, Name: "dev"}
	database.Create(&agent)
	agentID := agent.ID
	task := db.Task{CompanyID: comp.ID, ProjectID: &proj.ID, SprintID: sprint.ID, AgentID: &agentID, Title: "t", Status: "backlog", TaskType: "implement", Priority: "Normal"}
	database.Create(&task)

	archivePath, err := CreateBackup(basePath, database)
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	// Wipe rows (children before parents), then restore from the archive.
	for _, table := range []string{"tasks", "agents", "sprints", "projects", "companies"} {
		database.Exec("DELETE FROM " + table)
	}
	if err := RestoreBackup(archivePath, t.TempDir(), database); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	// Restored ids preserved.
	var gotComp db.Company
	if err := database.First(&gotComp, comp.ID).Error; err != nil {
		t.Fatalf("company %d not restored: %v", comp.ID, err)
	}

	// The critical assertion: a fresh auto-id insert must NOT collide with the
	// restored row. Before the sequence-realignment fix this failed with a
	// duplicate-key violation on the primary key.
	newComp := db.Company{Name: "Beta", ShortName: "beta"}
	if err := database.Create(&newComp).Error; err != nil {
		t.Fatalf("post-restore insert collided with restored ids (sequence not realigned): %v", err)
	}
	if newComp.ID <= comp.ID {
		t.Fatalf("new company id %d not past restored id %d", newComp.ID, comp.ID)
	}

	// And on the deeper tables too.
	newAgent := db.Agent{CompanyID: gotComp.ID, Name: "dev2"}
	if err := database.Create(&newAgent).Error; err != nil {
		t.Fatalf("post-restore agent insert collided: %v", err)
	}
}
