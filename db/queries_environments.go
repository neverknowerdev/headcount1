package db

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"agent-orchestrator/pkg/secrets"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DefaultEnvironmentName is the environment new tasks fall back to when they
// carry no explicit environment_id.
const DefaultEnvironmentName = "headcount1 cloud"

// builtinEnvironmentNames are seeded for every company, in this order.
var builtinEnvironmentNames = []string{DefaultEnvironmentName, "preview", "production"}

// envVarNameRe validates secret names: they become shell env vars, so they
// must be safe to place in a process environment (and unambiguous in docs).
var envVarNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateEnvVarName reports whether name is a legal env var name.
func ValidateEnvVarName(name string) error {
	if !envVarNameRe.MatchString(name) {
		return fmt.Errorf("invalid secret name %q: must match [A-Za-z_][A-Za-z0-9_]*", name)
	}
	return nil
}

// EnsureDefaultEnvironments seeds the builtin environments for a company if
// they don't exist yet, marking "headcount1 cloud" as the default. Idempotent;
// called lazily from every read path so pre-existing companies get seeded too.
func (q *Queries) EnsureDefaultEnvironments(ctx context.Context, companyID int32) error {
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, name := range builtinEnvironmentNames {
			env := Environment{
				CompanyID: companyID,
				Name:      name,
				Builtin:   true,
				IsDefault: name == DefaultEnvironmentName,
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "company_id"}, {Name: "name"}},
				DoNothing: true,
			}).Create(&env).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ListEnvironments returns a company's environments (builtin first, then by
// name), seeding the defaults on first call.
func (q *Queries) ListEnvironments(ctx context.Context, companyID int32) ([]Environment, error) {
	if err := q.EnsureDefaultEnvironments(ctx, companyID); err != nil {
		return nil, err
	}
	var envs []Environment
	err := q.db.WithContext(ctx).
		Where("company_id = ?", companyID).
		Order("builtin desc, name asc").
		Find(&envs).Error
	return envs, err
}

// GetEnvironment returns one environment by id.
func (q *Queries) GetEnvironment(ctx context.Context, id int32) (Environment, error) {
	var env Environment
	err := q.db.WithContext(ctx).First(&env, id).Error
	return env, err
}

// GetDefaultEnvironment returns the company's default environment, seeding
// the builtins if needed.
func (q *Queries) GetDefaultEnvironment(ctx context.Context, companyID int32) (Environment, error) {
	if err := q.EnsureDefaultEnvironments(ctx, companyID); err != nil {
		return Environment{}, err
	}
	var env Environment
	err := q.db.WithContext(ctx).
		Where("company_id = ? AND is_default", companyID).
		First(&env).Error
	return env, err
}

// CreateEnvironment adds a user-defined environment to a company.
func (q *Queries) CreateEnvironment(ctx context.Context, companyID int32, name string) (Environment, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Environment{}, fmt.Errorf("environment name is required")
	}
	env := Environment{CompanyID: companyID, Name: name}
	err := q.db.WithContext(ctx).Create(&env).Error
	return env, err
}

// RenameEnvironment renames a user-defined environment. Builtin environments
// are fixed: tasks and docs refer to them by name.
func (q *Queries) RenameEnvironment(ctx context.Context, id int32, name string) (Environment, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Environment{}, fmt.Errorf("environment name is required")
	}
	env, err := q.GetEnvironment(ctx, id)
	if err != nil {
		return Environment{}, err
	}
	if env.Builtin {
		return Environment{}, fmt.Errorf("builtin environments cannot be renamed")
	}
	env.Name = name
	err = q.db.WithContext(ctx).Save(&env).Error
	return env, err
}

// DeleteEnvironment removes a user-defined environment and (via FK cascade)
// its secrets. Tasks referencing it fall back to the default environment
// (their environment_id is SET NULL by the FK).
func (q *Queries) DeleteEnvironment(ctx context.Context, id int32) error {
	env, err := q.GetEnvironment(ctx, id)
	if err != nil {
		return err
	}
	if env.Builtin {
		return fmt.Errorf("builtin environments cannot be deleted")
	}
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// SQLite (glebarez) doesn't always enforce FK cascade/SET NULL on
		// AutoMigrate-created schemas; do both explicitly.
		if err := tx.Model(&Task{}).Where("environment_id = ?", id).Update("environment_id", nil).Error; err != nil {
			return err
		}
		if err := tx.Where("environment_id = ?", id).Delete(&EnvironmentSecret{}).Error; err != nil {
			return err
		}
		return tx.Delete(&Environment{}, id).Error
	})
}

// ListEnvironmentSecrets returns an environment's secrets with HasValue
// computed. Values stay sealed; the name is the only usable field.
func (q *Queries) ListEnvironmentSecrets(ctx context.Context, environmentID int32) ([]EnvironmentSecret, error) {
	var secretsRows []EnvironmentSecret
	err := q.db.WithContext(ctx).
		Where("environment_id = ?", environmentID).
		Order("name asc").
		Find(&secretsRows).Error
	for i := range secretsRows {
		secretsRows[i].HasValue = secretsRows[i].ValueEncrypted != ""
	}
	return secretsRows, err
}

// UpsertEnvironmentSecret creates or replaces a named secret in an
// environment. The plaintext is sealed here, at the point of write, under the
// calling user's DEK — returns secrets.ErrLocked if their vault is locked.
func (q *Queries) UpsertEnvironmentSecret(ctx context.Context, environmentID int32, userID int32, name, value string) (EnvironmentSecret, error) {
	if err := ValidateEnvVarName(name); err != nil {
		return EnvironmentSecret{}, err
	}
	sealed, err := secrets.Default().EncryptForUser(userID, value)
	if err != nil {
		return EnvironmentSecret{}, err
	}
	row := EnvironmentSecret{
		EnvironmentID:  environmentID,
		Name:           name,
		ValueEncrypted: sealed,
		UserID:         &userID,
	}
	err = q.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "environment_id"}, {Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "user_id", "updated_at"}),
	}).Create(&row).Error
	row.HasValue = row.ValueEncrypted != ""
	return row, err
}

// DeleteEnvironmentSecret removes one named secret from an environment.
func (q *Queries) DeleteEnvironmentSecret(ctx context.Context, environmentID int32, name string) error {
	return q.db.WithContext(ctx).
		Where("environment_id = ? AND name = ?", environmentID, name).
		Delete(&EnvironmentSecret{}).Error
}

// EnvironmentSecretsForTask resolves the environment whose secrets a task's
// runs receive (the task's own environment_id, or the company default) and
// returns that environment plus its secret rows (values still sealed —
// decrypt at the point of use).
func (q *Queries) EnvironmentSecretsForTask(ctx context.Context, task Task) (Environment, []EnvironmentSecret, error) {
	var env Environment
	var err error
	if task.EnvironmentID != nil {
		env, err = q.GetEnvironment(ctx, *task.EnvironmentID)
	} else {
		env, err = q.GetDefaultEnvironment(ctx, task.CompanyID)
	}
	if err != nil {
		return Environment{}, nil, err
	}
	rows, err := q.ListEnvironmentSecrets(ctx, env.ID)
	return env, rows, err
}
