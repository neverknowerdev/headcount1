package hindsight

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ourBank reports whether a bank id follows this app's naming convention
// (one bank per company).
func ourBank(bankID string) bool {
	return strings.HasPrefix(bankID, "company-")
}

// writeBankExport exports one bank (through the given tenant-scoped client)
// into dir as "<bank>.zip" (documents) + "<bank>.template.json" (config,
// mental models, directives — document-transfer does not carry these).
func writeBankExport(ctx context.Context, tc *Client, dir, bank string) error {
	data, eerr := tc.ExportDocuments(ctx, bank)
	if eerr != nil {
		log.Printf("hindsight: export bank %s failed: %v", bank, eerr)
		return nil // best-effort per bank
	}
	if werr := os.WriteFile(filepath.Join(dir, bank+".zip"), data, 0644); werr != nil {
		return werr
	}
	manifest, terr := tc.ExportBankTemplate(ctx, bank)
	if terr != nil {
		log.Printf("hindsight: export bank template %s failed (non-fatal, config/mental-models won't restore): %v", bank, terr)
		return nil
	}
	if werr := os.WriteFile(filepath.Join(dir, bank+".template.json"), manifest, 0644); werr != nil {
		return werr
	}
	return nil
}

// importBank restores one bank (through the given tenant-scoped client) from
// the "<srcBank>.zip"/".template.json" files in dir into dstBank. The template
// is applied first so the bank/config/mental-models exist before documents
// land; documents replace by id (idempotent). Returns false if no archive for
// srcBank exists in dir.
func importBank(ctx context.Context, tc *Client, dir, srcBank, dstBank string) bool {
	data, rerr := os.ReadFile(filepath.Join(dir, srcBank+".zip"))
	if rerr != nil {
		return false
	}
	if manifest, merr := os.ReadFile(filepath.Join(dir, srcBank+".template.json")); merr == nil {
		if ierr := tc.ImportBankTemplate(ctx, dstBank, manifest); ierr != nil {
			log.Printf("hindsight: import bank template %s failed (non-fatal): %v", dstBank, ierr)
		}
	}
	if ierr := tc.ImportDocuments(ctx, dstBank, data, "replace"); ierr != nil {
		log.Printf("hindsight: import bank %s failed: %v", dstBank, ierr)
	}
	return true
}

// ExportAllToDir dumps every app-owned memory bank (across all teams) into dir
// for the operator's whole-instance backup. Each company's bank lives in its
// team's tenant now, so this iterates the app's companies and routes each
// export through that company's team tenant — a plain ListBanks would only see
// the empty "default" tenant. Files are flat ("company-<id>.zip"): company ids
// are globally unique, and import re-derives the tenant from the DB.
func (s *Service) ExportAllToDir(ctx context.Context, dir string) error {
	c := s.client()
	if c == nil {
		return nil // memory disabled/unreachable — nothing to export
	}
	companies, err := s.q.ListCompanies(ctx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	// Remove stale exports so deleted banks don't resurrect on restore.
	if entries, rerr := os.ReadDir(dir); rerr == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".zip") || strings.HasSuffix(e.Name(), ".template.json") {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	for _, comp := range companies {
		if comp.TeamID == nil {
			continue
		}
		tc := c.WithTenant(TenantID(*comp.TeamID))
		if werr := writeBankExport(ctx, tc, dir, BankID(comp.ID)); werr != nil {
			return werr
		}
	}
	return nil
}

// ImportAllFromDir restores every "company-<id>.zip" transfer archive found in
// dir into its bank, routing each through the tenant of the company's current
// team (looked up in the app DB), then applies the matching template manifest.
// Called after a whole-instance backup archive was extracted (IDs preserved,
// so each archived company still maps to the same team/tenant).
func (s *Service) ImportAllFromDir(ctx context.Context, dir string) error {
	c := s.client()
	if c == nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".zip") {
			continue
		}
		bankID := strings.TrimSuffix(e.Name(), ".zip")
		cid, ok := CompanyIDFromBankID(bankID)
		if !ok {
			continue
		}
		comp, cerr := s.q.GetCompany(ctx, cid)
		if cerr != nil || comp.TeamID == nil {
			log.Printf("hindsight: skip import of %s: no company/team in DB", bankID)
			continue
		}
		tenant := TenantID(*comp.TeamID)
		s.EnsureTenant(ctx, tenant)
		importBank(ctx, c.WithTenant(tenant), dir, bankID, bankID)
	}
	return nil
}

// ExportTeamBanksToDir exports the given companies' banks (under the team's
// tenant) into dir, for inclusion in a per-tenant data export. companyIDs must
// all belong to teamID.
func (s *Service) ExportTeamBanksToDir(ctx context.Context, dir string, teamID int32, companyIDs []int32) error {
	c := s.client()
	if c == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	tc := c.WithTenant(TenantID(teamID))
	for _, cid := range companyIDs {
		if werr := writeBankExport(ctx, tc, dir, BankID(cid)); werr != nil {
			return werr
		}
	}
	return nil
}

// ImportTeamBanksFromDir imports memory for a set of companies remapped onto a
// destination team's tenant. remap maps each archived (source) company id to
// the new company id in this database, so a bank archived as "company-<old>"
// is restored as "company-<new>" under team-<dstTeamID> — bridging the
// company-id remapping the tenant importer performs.
func (s *Service) ImportTeamBanksFromDir(ctx context.Context, dir string, dstTeamID int32, remap map[int32]int32) error {
	c := s.client()
	if c == nil {
		return nil
	}
	tenant := TenantID(dstTeamID)
	s.EnsureTenant(ctx, tenant)
	tc := c.WithTenant(tenant)
	for oldID, newID := range remap {
		importBank(ctx, tc, dir, BankID(oldID), BankID(newID))
	}
	return nil
}

// RecoverFromExportDir restores memory banks into a freshly initialized
// backend from the most recent on-disk backup export (the "<bank>.zip" +
// "<bank>.template.json" files ExportAllToDir writes before every backup).
// This is the recovery path after a schema fallback: the previous schema
// can't be read by the installed hindsight-api at all, but the export on
// disk can be imported through Hindsight's own supported API — which
// re-embeds facts and restores bank config/mental models/directives
// consistently, unlike any raw copy between incompatibly-migrated schemas.
// Returns a human-readable summary for the memory status notice.
func (s *Service) RecoverFromExportDir(ctx context.Context, dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Sprintf("Restoring memories from the backup export failed: %v.", err)
	}
	banks := 0
	var newest time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".zip") || !ourBank(strings.TrimSuffix(e.Name(), ".zip")) {
			continue
		}
		banks++
		if info, ierr := e.Info(); ierr == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if banks == 0 {
		return "No backup export of the previous memories was found, so they were not carried over — they remain only in the previous database schema. Project docs are re-synced from the repos automatically."
	}
	ictx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := s.ImportAllFromDir(ictx, dir); err != nil {
		return fmt.Sprintf("Restoring memories from the backup export failed: %v.", err)
	}
	return fmt.Sprintf(
		"%d memory bank(s) were restored from the latest backup export (from %s). Project docs are re-synced from the repos automatically; anything memorized after that export remains only in the previous database schema.",
		banks, newest.Format("2006-01-02 15:04 MST"))
}
