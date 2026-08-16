package migrations

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"ariga.io/atlas/sql/migrate"
	"ariga.io/atlas/sql/postgres"
	"ariga.io/atlas/sql/schema"
	"ariga.io/atlas/sql/sqlite"
	"gorm.io/gorm"
)

const (
	revisionTable = "atlas_schema_revisions"
	// Legacy installations may already contain the initial 43 tables. Baseline
	// there, then execute the follow-up enum
	// migrations so existing databases receive the same validation as fresh
	// databases.
	baselineMigration = "20260816000043"
)

// Apply applies all pending embedded migrations for the database dialect.
// Existing databases are adopted by recording the initial schema as a
// baseline; new databases execute every table migration from an empty schema.
func Apply(ctx context.Context, database *sql.DB, dialect, operatorVersion string) error {
	if database == nil {
		return errors.New("database is nil")
	}

	drv, err := openDriver(database, dialect)
	if err != nil {
		return err
	}
	store := &revisionStore{db: database, dialect: dialect}
	if err := store.ensure(ctx); err != nil {
		return fmt.Errorf("initialize Atlas revision store: %w", err)
	}
	dir, err := embeddedDir(dialect)
	if err != nil {
		return err
	}
	defer dir.Close()

	// The revision table is created before Atlas inspects the database. Atlas
	// therefore sees a non-empty schema even on a brand-new database; the
	// application-table check below decides whether that means baseline or a
	// genuinely clean database.
	options := []migrate.ExecutorOption{migrate.WithOperatorVersion(operatorVersion)}
	revisions, err := store.ReadRevisions(ctx)
	if err != nil {
		return fmt.Errorf("read Atlas revisions: %w", err)
	}
	if len(revisions) == 0 {
		dirty, err := hasApplicationTables(ctx, database, dialect)
		if err != nil {
			return fmt.Errorf("inspect database before baseline: %w", err)
		}
		if dirty {
			// The first migration captures the schema already created by the
			// legacy schema path. Mark the complete initial set as applied
			// rather than attempting to recreate existing tables.
			options = append(options, migrate.WithBaselineVersion(baselineMigration))
		} else {
			// The revision table is created before Atlas inspects the database,
			// so a brand-new database is technically non-empty to Atlas.
			options = append(options, migrate.WithAllowDirty(true))
		}
	} else {
		// The revision table is expected to exist alongside the application
		// schema on every subsequent run.
		options = append(options, migrate.WithAllowDirty(true))
	}

	executor, err := migrate.NewExecutor(drv, dir, store, options...)
	if err != nil {
		return fmt.Errorf("create Atlas migration executor: %w", err)
	}
	if err := executor.ExecuteN(ctx, 0); err != nil && !errors.Is(err, migrate.ErrNoPendingFiles) {
		return fmt.Errorf("apply Atlas migrations: %w", err)
	}
	return nil
}

// ApplyGORM adapts a GORM database handle to the embedded migration runner.
// It exists for callers that already use GORM for queries; schema creation is
// still performed exclusively by Atlas migrations.
func ApplyGORM(database *gorm.DB, dialect, operatorVersion string) error {
	if database == nil {
		return errors.New("database is nil")
	}
	sqlDB, err := database.DB()
	if err != nil {
		return fmt.Errorf("get SQL database: %w", err)
	}
	return Apply(context.Background(), sqlDB, dialect, operatorVersion)
}

func hasApplicationTables(ctx context.Context, database *sql.DB, dialect string) (bool, error) {
	var present bool
	switch dialect {
	case "postgres":
		err := database.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM information_schema.tables
  WHERE table_schema = 'public' AND table_name <> $1
)`, revisionTable).Scan(&present)
		return present, err
	case "sqlite":
		err := database.QueryRowContext(ctx, `SELECT EXISTS (
  SELECT 1 FROM sqlite_master
  WHERE type = 'table' AND name <> ? AND name NOT LIKE 'sqlite_%'
)`, revisionTable).Scan(&present)
		return present, err
	default:
		return false, fmt.Errorf("unsupported database dialect %q", dialect)
	}
}

func openDriver(database *sql.DB, dialect string) (migrate.Driver, error) {
	switch dialect {
	case "postgres":
		driver, err := postgres.Open(database)
		if err != nil {
			return nil, fmt.Errorf("open Atlas PostgreSQL driver: %w", err)
		}
		return driver, nil
	case "sqlite":
		driver, err := sqlite.Open(database)
		if err != nil {
			return nil, fmt.Errorf("open Atlas SQLite driver: %w", err)
		}
		return driver, nil
	default:
		return nil, fmt.Errorf("unsupported database dialect %q", dialect)
	}
}

func embeddedDir(dialect string) (*migrate.MemDir, error) {
	entries, err := fs.ReadDir(Files, dialect)
	if err != nil {
		return nil, fmt.Errorf("read embedded %s migrations: %w", dialect, err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".up.sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("no embedded %s migrations found", dialect)
	}

	dir := migrate.OpenMemDir(fmt.Sprintf("headcount1-%s-%d", dialect, time.Now().UnixNano()))
	var files []migrate.File
	for _, name := range names {
		contents, err := fs.ReadFile(Files, path.Join(dialect, name))
		if err != nil {
			dir.Close()
			return nil, fmt.Errorf("read embedded migration %s: %w", name, err)
		}
		atlasName := strings.TrimSuffix(name, ".up.sql") + ".sql"
		files = append(files, migrate.NewLocalFile(atlasName, contents))
	}
	if err := dir.CopyFiles(files); err != nil {
		dir.Close()
		return nil, fmt.Errorf("prepare embedded migrations: %w", err)
	}
	return dir, nil
}

type revisionStore struct {
	db      *sql.DB
	dialect string
}

func (s *revisionStore) Ident() *migrate.TableIdent {
	if s.dialect == "postgres" {
		return &migrate.TableIdent{Name: revisionTable, Schema: "public"}
	}
	return &migrate.TableIdent{Name: revisionTable}
}

func (s *revisionStore) ensure(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS atlas_schema_revisions (
  version TEXT PRIMARY KEY,
  description TEXT NOT NULL,
  type INTEGER NOT NULL DEFAULT 2,
  applied INTEGER NOT NULL DEFAULT 0,
  total INTEGER NOT NULL DEFAULT 0,
  executed_at TIMESTAMP NOT NULL,
  execution_time BIGINT NOT NULL DEFAULT 0,
  error TEXT,
  error_stmt TEXT,
  hash TEXT NOT NULL,
  partial_hashes TEXT,
  operator_version TEXT NOT NULL DEFAULT ''
)`)
	return err
}

func (s *revisionStore) ReadRevisions(ctx context.Context) ([]*migrate.Revision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT version, description, type, applied, total, executed_at, execution_time, error, error_stmt, hash, partial_hashes, operator_version FROM atlas_schema_revisions ORDER BY version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var revisions []*migrate.Revision
	for rows.Next() {
		revision, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		revisions = append(revisions, revision)
	}
	return revisions, rows.Err()
}

func (s *revisionStore) ReadRevision(ctx context.Context, version string) (*migrate.Revision, error) {
	row := s.db.QueryRowContext(ctx, `SELECT version, description, type, applied, total, executed_at, execution_time, error, error_stmt, hash, partial_hashes, operator_version FROM atlas_schema_revisions WHERE version = `+s.placeholder(1), version)
	revision, err := scanRevision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, migrate.ErrRevisionNotExist
	}
	return revision, err
}

func (s *revisionStore) WriteRevision(ctx context.Context, revision *migrate.Revision) error {
	partialHashes, err := json.Marshal(revision.PartialHashes)
	if err != nil {
		return err
	}
	if revision.PartialHashes == nil {
		partialHashes = nil
	}
	args := []any{
		revision.Version, revision.Description, revision.Type, revision.Applied, revision.Total,
		revision.ExecutedAt, revision.ExecutionTime, nullableString(revision.Error), nullableString(revision.ErrorStmt),
		revision.Hash, nullableBytes(partialHashes), revision.OperatorVersion,
	}
	query := `INSERT INTO atlas_schema_revisions (version, description, type, applied, total, executed_at, execution_time, error, error_stmt, hash, partial_hashes, operator_version) VALUES (` + s.placeholders(12) + `) ON CONFLICT(version) DO UPDATE SET description = excluded.description, type = excluded.type, applied = excluded.applied, total = excluded.total, executed_at = excluded.executed_at, execution_time = excluded.execution_time, error = excluded.error, error_stmt = excluded.error_stmt, hash = excluded.hash, partial_hashes = excluded.partial_hashes, operator_version = excluded.operator_version`
	_, err = s.db.ExecContext(ctx, query, args...)
	return err
}

func (s *revisionStore) DeleteRevision(ctx context.Context, version string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM atlas_schema_revisions WHERE version = `+s.placeholder(1), version)
	return err
}

type scanner interface{ Scan(...any) error }

func scanRevision(row scanner) (*migrate.Revision, error) {
	var (
		revision                   migrate.Revision
		typ                        int
		execTime                   int64
		executed                   time.Time
		errText, stmtText, partial sql.NullString
	)
	if err := row.Scan(&revision.Version, &revision.Description, &typ, &revision.Applied, &revision.Total, &executed, &execTime, &errText, &stmtText, &revision.Hash, &partial, &revision.OperatorVersion); err != nil {
		return nil, err
	}
	revision.Type = migrate.RevisionType(typ)
	revision.ExecutedAt = executed
	revision.ExecutionTime = time.Duration(execTime)
	revision.Error = errText.String
	revision.ErrorStmt = stmtText.String
	if partial.Valid && partial.String != "" {
		if err := json.Unmarshal([]byte(partial.String), &revision.PartialHashes); err != nil {
			return nil, fmt.Errorf("decode partial hashes: %w", err)
		}
	}
	return &revision, nil
}

func (s *revisionStore) placeholder(n int) string {
	if s.dialect == "postgres" {
		return fmt.Sprintf("$%d", n)
	}
	return "?"
}

func (s *revisionStore) placeholders(n int) string {
	values := make([]string, n)
	for i := range values {
		values[i] = s.placeholder(i + 1)
	}
	return strings.Join(values, ", ")
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

var _ schema.ExecQuerier = (*sql.DB)(nil)
