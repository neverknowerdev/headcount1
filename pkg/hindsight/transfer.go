package hindsight

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// ourBank reports whether a bank id follows this app's naming conventions.
// The legacy "proj-"/"runs-" prefixes are still recognized so an old backup
// archive restores its banks (a startup migration then folds them into the
// current per-company bank — see MigrateLegacyBanks).
func ourBank(bankID string) bool {
	return strings.HasPrefix(bankID, "company-") ||
		strings.HasPrefix(bankID, "proj-") || strings.HasPrefix(bankID, "runs-")
}

// ExportAllToDir dumps every app-owned memory bank as a document-transfer ZIP
// archive ("<bank>.zip") plus a bank-template manifest ("<bank>.template.json"
// — config, mental models, directives; document-transfer does not carry
// these) into dir. Called before a backup archive is built so the memory
// layer travels inside the regular backup tarball.
func (s *Service) ExportAllToDir(ctx context.Context, dir string) error {
	c := s.client()
	if c == nil {
		return nil // memory disabled/unreachable — nothing to export
	}
	banks, err := c.ListBanks(ctx)
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
	for _, b := range banks {
		if !ourBank(b.BankID) {
			continue
		}
		data, eerr := c.ExportDocuments(ctx, b.BankID)
		if eerr != nil {
			log.Printf("hindsight: export bank %s failed: %v", b.BankID, eerr)
			continue
		}
		if werr := os.WriteFile(filepath.Join(dir, b.BankID+".zip"), data, 0644); werr != nil {
			return werr
		}
		manifest, terr := c.ExportBankTemplate(ctx, b.BankID)
		if terr != nil {
			log.Printf("hindsight: export bank template %s failed (non-fatal, config/mental-models won't restore): %v", b.BankID, terr)
			continue
		}
		if werr := os.WriteFile(filepath.Join(dir, b.BankID+".template.json"), manifest, 0644); werr != nil {
			return werr
		}
	}
	return nil
}

// ImportAllFromDir restores every "<bank>.zip" transfer archive found in dir
// into its bank, replacing documents that share ids, then applies the
// matching "<bank>.template.json" manifest (config, mental models,
// directives) if present. The manifest is imported first so the bank and its
// config exist before documents land — mental models then refresh
// themselves once retained memories consolidate. Called after a backup
// archive was extracted.
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
		if !ourBank(bankID) {
			continue
		}

		if manifest, rerr := os.ReadFile(filepath.Join(dir, bankID+".template.json")); rerr == nil {
			if ierr := c.ImportBankTemplate(ctx, bankID, manifest); ierr != nil {
				log.Printf("hindsight: import bank template %s failed (non-fatal): %v", bankID, ierr)
			}
		}

		data, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			log.Printf("hindsight: read archive %s failed: %v", e.Name(), rerr)
			continue
		}
		if ierr := c.ImportDocuments(ctx, bankID, data, "replace"); ierr != nil {
			log.Printf("hindsight: import bank %s failed: %v", bankID, ierr)
		}
	}
	return nil
}
