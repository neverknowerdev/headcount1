package db

import "gorm.io/gorm"

// EnsurePostgresEnumTypes creates enum types used by models before GORM runs
// AutoMigrate. PostgreSQL requires the type to exist before it can create the
// RunSnapshot table; other dialects keep using their native string-like type.
func EnsurePostgresEnumTypes(database *gorm.DB) error {
	if database == nil || database.Dialector.Name() != "postgres" {
		return nil
	}
	return database.Exec(`DO $$
BEGIN
  CREATE TYPE checkpoint_phase AS ENUM ('before_tools', 'after_tools');
EXCEPTION
  WHEN duplicate_object THEN NULL;
END $$;`).Error
}
