package hindsight

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// ourBank reports whether a bank id follows this app's naming conventions
// (project docs or company run-experience banks).
func ourBank(bankID string) bool {
	return strings.HasPrefix(bankID, "proj-") || strings.HasPrefix(bankID, "runs-")
}

// ExportAllToDir dumps every app-owned memory bank as a document-transfer ZIP
// archive ("<bank>.zip") into dir. Called before a backup archive is built so
// the memory layer travels inside the regular backup tarball.
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
			if strings.HasSuffix(e.Name(), ".zip") {
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
	}
	return nil
}

// ImportAllFromDir restores every "<bank>.zip" transfer archive found in dir
// into its bank, replacing documents that share ids. Called after a backup
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
