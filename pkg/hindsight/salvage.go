package hindsight

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver
)

// Schema salvage: when the memory backend falls back to a fresh schema
// because the previous one was migrated by an incompatible hindsight-api
// build (see Manager.Start), the data itself is usually fine — only the
// Alembic bookkeeping is unreadable. This module copies that data into the
// new schema so agents and the UI keep their memories.
//
// The copy is deliberately structure-agnostic so it survives version drift
// in either direction: it copies the intersection of tables and columns
// that exist in BOTH schemas, in foreign-key dependency order, skipping
// operational tables (queues, logs, caches) and Alembic state. Anything the
// new schema doesn't understand is simply left behind in the old schema,
// which is never modified.

// salvageSkipTables are operational tables that must not be carried across
// schema generations: Alembic bookkeeping (the very thing that diverged),
// in-flight job queues, request logs, and rebuildable caches.
var salvageSkipTables = map[string]bool{
	"alembic_version":         true,
	"async_operations":        true,
	"graph_maintenance_queue": true,
	"llm_requests":            true,
	"audit_log":               true,
	"webhooks":                true,
	"bank_stats_cache":        true,
}

// pg0Instance mirrors the instance.json that pg0 (hindsight-api's embedded
// Postgres manager) writes next to each running instance.
type pg0Instance struct {
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Database string `json:"database"`
}

// pg0DSN locates the embedded Postgres started by hindsight-api and returns
// a database/sql DSN for it. The instance dir is ~/.pg0/instances/<name>;
// hindsight-api names its instance "hindsight" but we glob to stay robust.
func pg0DSN() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	candidates, _ := filepath.Glob(filepath.Join(home, ".pg0", "instances", "*", "instance.json"))
	// Prefer the canonical instance name.
	sort.Slice(candidates, func(i, j int) bool {
		iCanon := filepath.Base(filepath.Dir(candidates[i])) == "hindsight"
		jCanon := filepath.Base(filepath.Dir(candidates[j])) == "hindsight"
		return iCanon && !jCanon
	})
	for _, path := range candidates {
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			continue
		}
		var inst pg0Instance
		if json.Unmarshal(data, &inst) != nil || inst.Port == 0 {
			continue
		}
		return fmt.Sprintf("postgres://%s:%s@127.0.0.1:%d/%s",
			inst.Username, inst.Password, inst.Port, inst.Database), nil
	}
	return "", fmt.Errorf("no pg0 instance.json found under ~/.pg0/instances")
}

// salvageSchemaData copies memory data from schema `from` into schema `to`
// inside the embedded Postgres. Returns the number of rows copied per table.
// Best-effort: tables that cannot be copied are logged and skipped, and the
// source schema is never written to.
func salvageSchemaData(ctx context.Context, from, to string) (map[string]int, error) {
	dsn, err := pg0DSN()
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("embedded postgres unreachable: %w", err)
	}
	return copySchemaData(ctx, db, from, to)
}

func copySchemaData(ctx context.Context, db *sql.DB, from, to string) (map[string]int, error) {
	tables, err := commonTables(ctx, db, from, to)
	if err != nil {
		return nil, err
	}
	if len(tables) == 0 {
		return nil, fmt.Errorf("schemas %q and %q share no tables", from, to)
	}
	ordered, err := orderByForeignKeys(ctx, db, to, tables)
	if err != nil {
		return nil, err
	}

	copied := make(map[string]int)
	var failed []string
	// FK-violating tables (e.g. a dependency that itself failed) get retried
	// once after everything else landed; still-failing tables are skipped.
	pending := ordered
	for pass := 0; pass < 2 && len(pending) > 0; pass++ {
		var stillPending []string
		for _, table := range pending {
			n, cerr := copyTable(ctx, db, from, to, table)
			if cerr != nil {
				stillPending = append(stillPending, table)
				continue
			}
			if n > 0 {
				copied[table] = int(n)
			}
		}
		pending = stillPending
	}
	for _, table := range pending {
		if _, cerr := copyTable(ctx, db, from, to, table); cerr != nil {
			failed = append(failed, table)
			log.Printf("hindsight: salvage: table %q could not be copied: %v", table, cerr)
		}
	}
	if len(copied) == 0 && len(failed) > 0 {
		return copied, fmt.Errorf("no tables could be copied (failed: %s)", strings.Join(failed, ", "))
	}
	return copied, nil
}

// copyTable inserts the column-intersection of one table from schema `from`
// into schema `to`, ignoring rows whose primary key already exists there.
func copyTable(ctx context.Context, db *sql.DB, from, to, table string) (int64, error) {
	cols, err := commonInsertableColumns(ctx, db, from, to, table)
	if err != nil {
		return 0, err
	}
	if len(cols) == 0 {
		return 0, fmt.Errorf("no shared columns")
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = pgQuoteIdent(c)
	}
	colList := strings.Join(quoted, ", ")
	stmt := fmt.Sprintf(`INSERT INTO %s.%s (%s) SELECT %s FROM %s.%s ON CONFLICT DO NOTHING`,
		pgQuoteIdent(to), pgQuoteIdent(table), colList, colList, pgQuoteIdent(from), pgQuoteIdent(table))
	res, err := db.ExecContext(ctx, stmt)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// commonTables returns base tables present in both schemas, minus the
// operational skip-list.
func commonTables(ctx context.Context, db *sql.DB, from, to string) ([]string, error) {
	list := func(schema string) (map[string]bool, error) {
		rows, err := db.QueryContext(ctx,
			`SELECT table_name FROM information_schema.tables WHERE table_schema = $1 AND table_type = 'BASE TABLE'`, schema)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var t string
			if err := rows.Scan(&t); err != nil {
				return nil, err
			}
			out[t] = true
		}
		return out, rows.Err()
	}
	fromTables, err := list(from)
	if err != nil {
		return nil, err
	}
	toTables, err := list(to)
	if err != nil {
		return nil, err
	}
	var common []string
	for t := range fromTables {
		if toTables[t] && !salvageSkipTables[t] {
			common = append(common, t)
		}
	}
	sort.Strings(common)
	return common, nil
}

// commonInsertableColumns returns columns present in both schemas' copies of
// a table, excluding generated columns in the target (they can't be written).
func commonInsertableColumns(ctx context.Context, db *sql.DB, from, to, table string) ([]string, error) {
	cols := func(schema string, excludeGenerated bool) (map[string]bool, error) {
		rows, err := db.QueryContext(ctx,
			`SELECT column_name, is_generated FROM information_schema.columns WHERE table_schema = $1 AND table_name = $2`, schema, table)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]bool{}
		for rows.Next() {
			var name, generated string
			if err := rows.Scan(&name, &generated); err != nil {
				return nil, err
			}
			if excludeGenerated && generated == "ALWAYS" {
				continue
			}
			out[name] = true
		}
		return out, rows.Err()
	}
	fromCols, err := cols(from, false)
	if err != nil {
		return nil, err
	}
	toCols, err := cols(to, true)
	if err != nil {
		return nil, err
	}
	var common []string
	for c := range fromCols {
		if toCols[c] {
			common = append(common, c)
		}
	}
	sort.Strings(common)
	return common, nil
}

// orderByForeignKeys topologically sorts tables so referenced tables load
// before referencing ones (FK graph of the target schema). Cycles and
// self-references keep their relative alphabetical order and rely on the
// retry pass in copySchemaData.
func orderByForeignKeys(ctx context.Context, db *sql.DB, schema string, tables []string) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT tc.table_name, ccu.table_name AS referenced
		FROM information_schema.table_constraints tc
		JOIN information_schema.constraint_column_usage ccu
		  ON tc.constraint_name = ccu.constraint_name AND tc.table_schema = ccu.table_schema
		WHERE tc.constraint_type = 'FOREIGN KEY' AND tc.table_schema = $1`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	inSet := map[string]bool{}
	for _, t := range tables {
		inSet[t] = true
	}
	deps := map[string][]string{} // table -> tables it references
	for rows.Next() {
		var table, referenced string
		if err := rows.Scan(&table, &referenced); err != nil {
			return nil, err
		}
		if table != referenced && inSet[table] && inSet[referenced] {
			deps[table] = append(deps[table], referenced)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var ordered []string
	visited := map[string]int{} // 0=unseen 1=visiting 2=done
	var visit func(t string)
	visit = func(t string) {
		if visited[t] != 0 {
			return
		}
		visited[t] = 1
		refs := deps[t]
		sort.Strings(refs)
		for _, r := range refs {
			if visited[r] == 1 {
				continue // cycle — retry pass handles ordering fallout
			}
			visit(r)
		}
		visited[t] = 2
		ordered = append(ordered, t)
	}
	for _, t := range tables {
		visit(t)
	}
	return ordered, nil
}

// totalRows sums a per-table copy report.
func totalRows(copied map[string]int) int {
	total := 0
	for _, n := range copied {
		total += n
	}
	return total
}

func pgQuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
