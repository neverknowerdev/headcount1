# Database migrations

The `postgres/` and `sqlite/` directories contain paired Atlas migrations for
the two supported database engines. The migration versions are intentionally
identical, but the SQL is dialect-specific: PostgreSQL uses schema-qualified
tables and sequences (`serial`/`bigserial`), while SQLite requires
`INTEGER PRIMARY KEY AUTOINCREMENT`. Atlas executes migration SQL as written;
it does not translate one dialect into another.

Keep every new migration version in both directories. Domain enums use
database-level `CHECK` validation with the same allowed values in both engines,
which gives the application the same validation without relying on a
PostgreSQL-only `CREATE TYPE`. PostgreSQL uses named constraints; SQLite uses
guard columns because SQLite cannot add a table constraint in place.
