package db

import "gorm.io/gorm"

// EnsurePostgresEnumTypes keeps the legacy checkpoint_phase type available for
// upgrades from the pre-JSON recovery schema. New Run rows store checkpoint
// phase inside the JSONB recovery document, so this is intentionally a no-op
// for the current schema beyond preserving compatibility with old databases.
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
