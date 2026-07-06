package hindsight

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"agent-orchestrator/db"
)

// migrationMarker is written once bank consolidation has run, so restarts
// don't repeat it. It lives next to the export-for-backup directory.
func migrationMarker(basePath string) string {
	return filepath.Join(basePath, "data", "hindsight", ".bank-v2-migrated")
}

// MigrateLegacyBanks folds the old per-project docs bank ("proj-<id>") and
// per-company run-experience bank ("runs-<id>") into the single per-company
// bank ("company-<id>"). Idempotent and safe to call on every startup: it
// no-ops once the marker file exists, and no-ops per-company when no legacy
// bank is found for it.
//
// Docs are not carried over via document-transfer — they are cheaply
// re-derivable from the project repos, so this deletes the tracking rows and
// lets the normal doc-sync path (SyncProjectDocs) re-ingest everything into
// the new bank on the next sync. Run experience (harder to regenerate) is
// carried over via the document-transfer export/import API, which preserves
// document ids, so no run memory is lost.
func (s *Service) MigrateLegacyBanks(ctx context.Context, basePath string) error {
	c := s.client()
	if c == nil {
		return fmt.Errorf("hindsight not available")
	}
	marker := migrationMarker(basePath)
	if _, err := os.Stat(marker); err == nil {
		return nil // already migrated
	}

	banks, err := c.ListBanks(ctx)
	if err != nil {
		return err
	}
	legacy := make(map[string]bool, len(banks))
	for _, b := range banks {
		if strings.HasPrefix(b.BankID, "proj-") || strings.HasPrefix(b.BankID, "runs-") {
			legacy[b.BankID] = true
		}
	}
	if len(legacy) == 0 {
		return s.writeMigrationMarker(marker)
	}

	companies, err := s.q.ListCompanies(ctx)
	if err != nil {
		return err
	}
	for _, company := range companies {
		if err := s.migrateCompanyBanks(ctx, company, legacy); err != nil {
			log.Printf("hindsight: migration for company %d failed (will retry next startup): %v", company.ID, err)
			return err // leave the marker unwritten so we retry
		}
	}
	return s.writeMigrationMarker(marker)
}

func (s *Service) migrateCompanyBanks(ctx context.Context, company db.Company, legacy map[string]bool) error {
	c := s.client()
	target := BankID(company.ID)

	// Run experience: carry over via document-transfer (preserves document ids).
	runsBank := fmt.Sprintf("runs-%d", company.ID)
	if legacy[runsBank] {
		data, err := c.ExportDocuments(ctx, runsBank)
		if err != nil {
			return fmt.Errorf("export %s: %w", runsBank, err)
		}
		if err := c.ImportDocuments(ctx, target, data, "skip"); err != nil {
			return fmt.Errorf("import %s into %s: %w", runsBank, target, err)
		}
		if err := c.DeleteBank(ctx, runsBank); err != nil {
			log.Printf("hindsight: failed to delete legacy bank %s (non-fatal): %v", runsBank, err)
		}
		log.Printf("hindsight: migrated run experience %s -> %s", runsBank, target)
	}

	// Docs: cheaper and more reliable to re-derive from the repo than to
	// carry over (old document ids lack the project prefix the new bank
	// needs). Wipe tracking rows so SyncProjectDocs treats every file as new.
	projects, err := s.q.ListProjectsByCompany(ctx, company.ID)
	if err != nil {
		return err
	}
	for _, project := range projects {
		projBank := fmt.Sprintf("proj-%d", project.ID)
		if !legacy[projBank] {
			continue
		}
		known, kerr := s.q.ListHindsightDocsByProject(ctx, project.ID)
		if kerr != nil {
			return kerr
		}
		for _, d := range known {
			_ = s.q.DeleteHindsightDoc(ctx, project.ID, d.Path)
		}
		if err := c.DeleteBank(ctx, projBank); err != nil {
			log.Printf("hindsight: failed to delete legacy bank %s (non-fatal): %v", projBank, err)
		}
		log.Printf("hindsight: cleared legacy doc tracking for project %d (%s) — will re-ingest into %s", project.ID, projBank, target)
	}
	return nil
}

func (s *Service) writeMigrationMarker(marker string) error {
	if err := os.MkdirAll(filepath.Dir(marker), 0755); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte("migrated\n"), 0644)
}
