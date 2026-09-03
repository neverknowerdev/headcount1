package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// CreatePostgresShadowSchema clones the current public tables into a uniquely
// named schema. It is intentionally explicit and scoped to the generated
// schema name; no caller can accidentally drop public data through this API.
func CreatePostgresShadowSchema(ctx context.Context, database *sql.DB, schemaName string) error {
	if database == nil {
		return fmt.Errorf("database is nil")
	}
	if !validSchemaName(schemaName) || schemaName == "public" {
		return fmt.Errorf("invalid shadow schema %q", schemaName)
	}
	qSchema := quoteIdent(schemaName)
	if _, err := database.ExecContext(ctx, "CREATE SCHEMA "+qSchema); err != nil {
		return err
	}
	rows, err := database.QueryContext(ctx, `SELECT tablename FROM pg_catalog.pg_tables WHERE schemaname = 'public' ORDER BY tablename`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return err
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, table := range tables {
		if _, err := database.ExecContext(ctx, "CREATE TABLE "+qSchema+"."+quoteIdent(table)+" (LIKE \"public\"."+quoteIdent(table)+" INCLUDING ALL)"); err != nil {
			return fmt.Errorf("clone table %s: %w", table, err)
		}
		if _, err := database.ExecContext(ctx, "INSERT INTO "+qSchema+"."+quoteIdent(table)+" SELECT * FROM \"public\"."+quoteIdent(table)); err != nil {
			return fmt.Errorf("copy table %s: %w", table, err)
		}
	}
	return nil
}

func DropPostgresShadowSchema(ctx context.Context, database *sql.DB, schemaName string) error {
	if database == nil {
		return fmt.Errorf("database is nil")
	}
	if !validSchemaName(schemaName) || schemaName == "public" {
		return fmt.Errorf("invalid shadow schema %q", schemaName)
	}
	_, err := database.ExecContext(ctx, "DROP SCHEMA "+quoteIdent(schemaName)+" CASCADE")
	return err
}

// PostgresSearchPath adds a validated shadow schema to an existing DSN. It
// preserves all query parameters and never accepts a caller-provided raw SQL
// fragment as a schema name.
func PostgresSearchPath(dsn, schemaName string) (string, error) {
	if !validSchemaName(schemaName) || schemaName == "public" {
		return "", fmt.Errorf("invalid shadow schema %q", schemaName)
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "search_path=" + schemaName + ",public", nil
}
