-- SQLite INTEGER PRIMARY KEY already supplies row IDs. This migration is a
-- paired no-op for PostgreSQL's legacy-schema sequence repair.
SELECT 1;
