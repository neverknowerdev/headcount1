package endpoints_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"agent-orchestrator/db"
	endpoints "agent-orchestrator/server/controllers"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func setupSyncTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, database.AutoMigrate(
		&db.Company{}, &db.Project{}, &db.Agent{}, &db.Sprint{},
		&db.LLMProvider{}, &db.MCPServer{}, &db.Task{}, &db.Comment{},
	))
	return database
}

// TestSyncCreatesCompanyWhenPreservedIDIsTaken reproduces the startup failure
// where companies/{name}/settings.yml preserves a company_id written by a
// different DB: the ID already belongs to another company here, and the
// explicit-ID insert used to fail with "UNIQUE constraint failed:
// companies.id", leaving the on-disk company permanently missing from the DB.
func TestSyncCreatesCompanyWhenPreservedIDIsTaken(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("E2E_PAPERCLIP_HOME", tmp)
	base := filepath.Join(tmp, ".paperclip2")

	database := setupSyncTestDB(t)
	// The DB already has a company that owns ID 1.
	dec := db.Company{Name: "DEC", ShortName: "DEC", Color: "#000000"}
	require.NoError(t, database.Create(&dec).Error)
	require.Equal(t, int32(1), dec.ID)

	// On disk: a company unknown to this DB whose settings.yml claims ID 1
	// (written against some other DB, e.g. another worktree or a wiped DB).
	// Companies are discovered from data/{shortName}; their settings.yml
	// lives under companies/{shortName}.
	cgDir := filepath.Join(base, "companies", "cgtest")
	require.NoError(t, os.MkdirAll(cgDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(base, "data", "cgtest"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cgDir, "settings.yml"),
		[]byte("company_id: 1\nname: Codegraph Test Co\nshort_name: cgtest\n"), 0644))

	api := endpoints.NewAPI(database, nil, nil)
	require.NoError(t, api.SyncDBWithFilesystem(context.Background()))

	var created db.Company
	require.NoError(t, database.Where("short_name = ?", "cgtest").First(&created).Error,
		"company found on disk must be created even when its preserved ID is taken")
	assert.Equal(t, "Codegraph Test Co", created.Name)
	assert.NotEqual(t, int32(1), created.ID, "must not steal another company's ID")

	// The original owner of ID 1 is untouched.
	var owner db.Company
	require.NoError(t, database.First(&owner, 1).Error)
	assert.Equal(t, "DEC", owner.ShortName)

	// settings.yml now records the ID the company actually got, so later
	// syncs resolve it consistently.
	data, err := os.ReadFile(filepath.Join(cgDir, "settings.yml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), fmt.Sprintf("company_id: %d", created.ID))
}

// TestSyncPreservesIDWhenFree verifies the preserved-ID path still works when
// the ID is available: the company keeps the ID from settings.yml.
func TestSyncPreservesIDWhenFree(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("E2E_PAPERCLIP_HOME", tmp)
	base := filepath.Join(tmp, ".paperclip2")

	database := setupSyncTestDB(t)

	cgDir := filepath.Join(base, "companies", "cgtest")
	require.NoError(t, os.MkdirAll(cgDir, 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(base, "data", "cgtest"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(cgDir, "settings.yml"),
		[]byte("company_id: 42\nname: Codegraph Test Co\nshort_name: cgtest\n"), 0644))

	api := endpoints.NewAPI(database, nil, nil)
	require.NoError(t, api.SyncDBWithFilesystem(context.Background()))

	var created db.Company
	require.NoError(t, database.Where("short_name = ?", "cgtest").First(&created).Error)
	assert.Equal(t, int32(42), created.ID, "a free preserved ID must be kept")
}
