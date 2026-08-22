package migrations

// This file contains the small amount of migration policy that sits around
// Atlas. Atlas owns normal forward application and its revision bookkeeping;
// this package owns release-to-release reconciliation, because a binary may be
// booting with a migration branch that differs from the one that last ran.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"ariga.io/atlas/sql/migrate"
)

const manifestDir = "deploy/migrations"

// Migration is the release manifest entry for one migration version.
// AtlasHash is the exact hash stored in atlas_schema_revisions; the SHA fields
// make the persisted manifest independently inspectable by operators.
type Migration struct {
	Version     string `json:"version"`
	Description string `json:"description"`
	AtlasHash   string `json:"atlas_hash"`
	UpSHA256    string `json:"up_sha256"`
	DownSHA256  string `json:"down_sha256,omitempty"`
	UpSQL       string `json:"up_sql"`
	DownSQL     string `json:"down_sql,omitempty"`
	Reversible  bool   `json:"reversible"`
}

type Manifest struct {
	Dialect    string      `json:"dialect"`
	Generated  time.Time   `json:"generated_at"`
	Migrations []Migration `json:"migrations"`
}

// AppliedRevision is the part of an Atlas revision that reconciliation needs.
type AppliedRevision struct {
	Version       string
	Description   string
	Hash          string
	Applied       int
	Total         int
	Error         string
	ErrorStmt     string
	PartialHashes []string
}

type ReconcilePlan struct {
	CommonVersion string
	Rollback      []Migration
	Apply         []Migration
}

type HistoryMismatchError struct {
	Version       string
	AppliedHash   string
	CandidateHash string
}

func (e *HistoryMismatchError) Error() string {
	return fmt.Sprintf("migration history changed at %s: database hash %q, candidate hash %q", e.Version, e.AppliedHash, e.CandidateHash)
}

type IrreversibleMigrationError struct {
	Version string
}

func (e *IrreversibleMigrationError) Error() string {
	return fmt.Sprintf("migration %s cannot be automatically rolled back: its down migration is marked irreversible", e.Version)
}

type MigrationError struct {
	Phase             string
	Version           string
	Statement         string
	StatementNo       int
	StatementPosition int
	Cause             error
}

func (e *MigrationError) Error() string {
	where := e.Phase
	if e.Version != "" {
		where += " migration " + e.Version
	}
	if e.StatementNo > 0 {
		where += fmt.Sprintf(" statement %d", e.StatementNo)
	}
	if e.StatementPosition > 0 {
		where += fmt.Sprintf(" at byte %d", e.StatementPosition)
	}
	if e.Statement != "" {
		return fmt.Sprintf("%s failed: %v; statement: %s", where, e.Cause, e.Statement)
	}
	return fmt.Sprintf("%s failed: %v", where, e.Cause)
}

func (e *MigrationError) Unwrap() error { return e.Cause }

// BuildManifest reads both up and down files. Down files are deliberately
// retained in the manifest so a previous release can be restored even after a
// candidate branch has removed one of its migration files.
func BuildManifest(dialect string) (Manifest, error) {
	fsEntries, err := Files.ReadDir(dialect)
	if err != nil {
		return Manifest{}, fmt.Errorf("read embedded %s migrations: %w", dialect, err)
	}
	var upNames []string
	for _, entry := range fsEntries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") {
			upNames = append(upNames, entry.Name())
		}
	}
	sort.Strings(upNames)
	if len(upNames) == 0 {
		return Manifest{}, fmt.Errorf("no embedded %s migrations found", dialect)
	}

	files := make([]migrate.File, 0, len(upNames))
	contents := make(map[string][]byte, len(upNames))
	for _, name := range upNames {
		b, err := Files.ReadFile(filepath.Join(dialect, name))
		if err != nil {
			return Manifest{}, fmt.Errorf("read embedded migration %s: %w", name, err)
		}
		atlasName := strings.TrimSuffix(name, ".up.sql") + ".sql"
		files = append(files, migrate.NewLocalFile(atlasName, b))
		contents[name] = b
	}
	hashes, err := migrate.NewHashFile(files)
	if err != nil {
		return Manifest{}, fmt.Errorf("hash embedded migrations: %w", err)
	}

	manifest := Manifest{Dialect: dialect, Generated: time.Now().UTC(), Migrations: make([]Migration, 0, len(upNames))}
	for _, name := range upNames {
		base := strings.TrimSuffix(name, ".up.sql")
		parts := strings.SplitN(base, "_", 2)
		if len(parts) != 2 {
			return Manifest{}, fmt.Errorf("invalid migration filename %q", name)
		}
		version, description := parts[0], parts[1]
		downName := base + ".down.sql"
		down, err := Files.ReadFile(filepath.Join(dialect, downName))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Manifest{}, fmt.Errorf("read embedded migration %s: %w", downName, err)
		}
		up := contents[name]
		atlasHash, err := hashes.SumByName(base + ".sql")
		if err != nil {
			return Manifest{}, fmt.Errorf("hash migration %s: %w", name, err)
		}
		manifest.Migrations = append(manifest.Migrations, Migration{
			Version:     version,
			Description: description,
			AtlasHash:   atlasHash,
			UpSHA256:    digest(up),
			DownSHA256:  digest(down),
			UpSQL:       string(up),
			DownSQL:     string(down),
			Reversible:  len(strings.TrimSpace(string(down))) > 0 && !strings.Contains(strings.ToLower(string(down)), "irreversible"),
		})
	}
	return manifest, nil
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (m Manifest) Entry(version string) (Migration, bool) {
	for _, entry := range m.Migrations {
		if entry.Version == version {
			return entry, true
		}
	}
	return Migration{}, false
}

func (m Manifest) At(version string) int {
	for i, entry := range m.Migrations {
		if entry.Version == version {
			return i
		}
	}
	return -1
}

func ManifestPath(basePath, dialect string) string {
	return filepath.Join(basePath, manifestDir, dialect+".json")
}

func SaveManifest(basePath string, manifest Manifest) error {
	path := ManifestPath(basePath, manifest.Dialect)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".migration-manifest-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func LoadManifest(basePath, dialect string) (Manifest, error) {
	b, err := os.ReadFile(ManifestPath(basePath, dialect))
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(b, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode migration manifest: %w", err)
	}
	return manifest, nil
}

func ReadApplied(ctx context.Context, database *sql.DB, dialect string) ([]AppliedRevision, error) {
	store := &revisionStore{db: database, dialect: dialect}
	revisions, err := store.ReadRevisions(ctx)
	if err != nil {
		return nil, err
	}
	applied := make([]AppliedRevision, 0, len(revisions))
	for _, r := range revisions {
		applied = append(applied, AppliedRevision{
			Version: r.Version, Description: r.Description, Hash: r.Hash,
			Applied: r.Applied, Total: r.Total, Error: r.Error,
			ErrorStmt: r.ErrorStmt, PartialHashes: append([]string(nil), r.PartialHashes...),
		})
	}
	return applied, nil
}

// PlanReconciliation computes the common prefix between the database history
// and the candidate history. rollbackManifest is the last known-good release;
// candidate is included too because it contains down SQL for newly attempted
// migrations that never existed in the previous release.
func PlanReconciliation(applied []AppliedRevision, candidate, rollbackManifest Manifest) (ReconcilePlan, error) {
	for _, revision := range applied {
		if revision.Applied != revision.Total {
			return ReconcilePlan{}, &MigrationError{
				Phase: "inspect", Version: revision.Version, Statement: revision.ErrorStmt,
				StatementNo: revision.Applied + 1,
				Cause:       fmt.Errorf("revision is partial (%d of %d): %s", revision.Applied, revision.Total, revision.Error),
			}
		}
	}

	common := 0
	for common < len(applied) && common < len(candidate.Migrations) {
		current := applied[common]
		target := candidate.Migrations[common]
		if current.Version != target.Version {
			break
		}
		if current.Hash != target.AtlasHash {
			return ReconcilePlan{}, &HistoryMismatchError{Version: current.Version, AppliedHash: current.Hash, CandidateHash: target.AtlasHash}
		}
		common++
	}

	plan := ReconcilePlan{Apply: append([]Migration(nil), candidate.Migrations[common:]...)}
	if common == len(applied) {
		if common > 0 {
			plan.CommonVersion = applied[common-1].Version
		}
		return plan, nil
	}
	if common > 0 {
		plan.CommonVersion = applied[common-1].Version
	}
	for i := len(applied) - 1; i >= common; i-- {
		entry, ok := candidate.Entry(applied[i].Version)
		if !ok {
			entry, ok = rollbackManifest.Entry(applied[i].Version)
		}
		if !ok {
			return ReconcilePlan{}, fmt.Errorf("migration %s is applied in the database but neither release contains its down migration", applied[i].Version)
		}
		if !entry.Reversible {
			return ReconcilePlan{}, &IrreversibleMigrationError{Version: entry.Version}
		}
		plan.Rollback = append(plan.Rollback, entry)
	}
	return plan, nil
}

func ApplyDown(ctx context.Context, database *sql.DB, dialect string, rollback []Migration) error {
	for _, migration := range rollback {
		if !migration.Reversible {
			return &IrreversibleMigrationError{Version: migration.Version}
		}
		statements, err := migrate.Stmts(migration.DownSQL)
		if err != nil {
			return &MigrationError{Phase: "down-parse", Version: migration.Version, Cause: err}
		}
		tx, err := database.BeginTx(ctx, nil)
		if err != nil {
			return &MigrationError{Phase: "down-begin", Version: migration.Version, Cause: err}
		}
		failed := func(phase string, stmtNo int, statement string, cause error) error {
			_ = tx.Rollback()
			return &MigrationError{Phase: phase, Version: migration.Version, StatementNo: stmtNo, Statement: statement, Cause: cause}
		}
		for i, statement := range statements {
			if _, err := tx.ExecContext(ctx, statement.Text); err != nil {
				return failed("down", i+1, statement.Text, err)
			}
		}
		table := revisionTable
		placeholder := "?"
		if dialect == "postgres" {
			table = "public." + table
			placeholder = "$1"
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE version = "+placeholder, migration.Version); err != nil {
			return failed("down-history", 0, "", err)
		}
		if err := tx.Commit(); err != nil {
			return &MigrationError{Phase: "down-commit", Version: migration.Version, Cause: err}
		}
	}
	return nil
}

// RollbackCandidate restores the last persisted migration manifest after a
// candidate failed during startup. Normal candidate migration execution is
// transactional, so this is primarily for a candidate that completed some
// down/up work before a later phase failed or for legacy dirty histories.
func RollbackCandidate(ctx context.Context, database *sql.DB, dialect, basePath string, candidate Manifest) error {
	previous, err := LoadManifest(basePath, dialect)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	applied, err := ReadApplied(ctx, database, dialect)
	if err != nil {
		return err
	}
	plan, err := PlanReconciliation(applied, previous, candidate)
	if err != nil {
		return err
	}
	if err := ApplyDown(ctx, database, dialect, plan.Rollback); err != nil {
		return err
	}
	return SaveManifest(basePath, previous)
}
