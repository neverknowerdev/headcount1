package backup

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"agent-orchestrator/db"
)

// TestTenantExportImportInvokesMemoryHooks asserts the per-tenant export/import
// drives the Hindsight memory hooks with the right team scope and company-id
// remap: export sees the source team + its company ids; import sees the
// destination team + a map from each archived company id to its new id. It also
// round-trips a file the export hook writes to prove memory rides the archive.
func TestTenantExportImportInvokesMemoryHooks(t *testing.T) {
	// Package-level hooks — set for this test, restored after.
	prevExport, prevImport := MemoryExportHook, MemoryImportHook
	defer func() { MemoryExportHook, MemoryImportHook = prevExport, prevImport }()

	var mu sync.Mutex
	var exportTeam int32
	var exportCompanies []int32
	var importTeam int32
	var importRemap map[int32]int32
	var importSawFile bool

	MemoryExportHook = func(ctx context.Context, dir string, srcTeamID int32, companyIDs []int32) error {
		mu.Lock()
		defer mu.Unlock()
		exportTeam = srcTeamID
		exportCompanies = append([]int32(nil), companyIDs...)
		// Write a marker so the archive carries a memory/ payload to extract.
		return os.WriteFile(filepath.Join(dir, "company-marker.zip"), []byte("mem"), 0644)
	}
	MemoryImportHook = func(ctx context.Context, dir string, dstTeamID int32, remap map[int32]int32) error {
		mu.Lock()
		defer mu.Unlock()
		importTeam = dstTeamID
		importRemap = remap
		if _, err := os.Stat(filepath.Join(dir, "company-marker.zip")); err == nil {
			importSawFile = true
		}
		return nil
	}

	ctx := context.Background()
	srcBase := t.TempDir()
	database := openTestDB(t, t.TempDir())

	srcUser := db.User{Email: "owner@acme.io"}
	database.Create(&srcUser)
	srcTeam := db.Team{Name: "Acme"}
	database.Create(&srcTeam)
	database.Create(&db.TeamMember{TeamID: srcTeam.ID, UserID: srcUser.ID, Role: db.TeamRoleOwner})
	comp := db.Company{Name: "Acme", ShortName: "acme", TeamID: &srcTeam.ID, UserID: &srcUser.ID}
	database.Create(&comp)

	var buf bytes.Buffer
	if err := ExportTenant(ctx, &buf, srcBase, database, srcUser.ID); err != nil {
		t.Fatalf("ExportTenant: %v", err)
	}
	archivePath := filepath.Join(t.TempDir(), "tenant.tar.gz")
	os.WriteFile(archivePath, buf.Bytes(), 0644)

	mu.Lock()
	if exportTeam != srcTeam.ID {
		t.Errorf("export hook team = %d, want %d", exportTeam, srcTeam.ID)
	}
	if len(exportCompanies) != 1 || exportCompanies[0] != comp.ID {
		t.Errorf("export hook companies = %v, want [%d]", exportCompanies, comp.ID)
	}
	mu.Unlock()

	// Import into a different team.
	targetDB := openTestDB(t, t.TempDir())
	impUser := db.User{Email: "importer@new.io"}
	targetDB.Create(&impUser)
	impTeam := db.Team{Name: "Importer"}
	targetDB.Create(&impTeam)
	targetDB.Create(&db.TeamMember{TeamID: impTeam.ID, UserID: impUser.ID, Role: db.TeamRoleOwner})

	if _, err := ImportTenant(ctx, archivePath, t.TempDir(), targetDB, impUser.ID, impTeam.ID); err != nil {
		t.Fatalf("ImportTenant: %v", err)
	}

	var imported db.Company
	if err := targetDB.Where("short_name = ? AND user_id = ?", "acme", impUser.ID).First(&imported).Error; err != nil {
		t.Fatalf("imported company not found: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if importTeam != impTeam.ID {
		t.Errorf("import hook team = %d, want %d", importTeam, impTeam.ID)
	}
	if got := importRemap[comp.ID]; got != imported.ID {
		t.Errorf("import remap[%d] = %d, want new company id %d", comp.ID, got, imported.ID)
	}
	if !importSawFile {
		t.Error("import hook did not see the exported memory/ payload from the archive")
	}
}
