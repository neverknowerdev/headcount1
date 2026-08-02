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

// DefaultEnvironmentName is the company-level platform environment whose
// entries agent runs receive as shell env vars.
const DefaultEnvironmentName = "headcount1 cloud"

// Environment entry kinds. Both are sealed at rest; the kind matters at the
// boundaries (redaction, and how connectors push them to deploy targets).
const (
	EnvEntrySecret   = "secret"
	EnvEntryVariable = "variable"
)

// Connector providers.
const (
	ConnectorVercel = "vercel"
	ConnectorGitHub = "github"
)

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

// EnsureDefaultEnvironments seeds the company-level platform environment
// ("headcount1 cloud") if it doesn't exist yet. Idempotent; called lazily
// from every read path so pre-existing companies get seeded too. It also
// cleans up the formerly seeded company-level "preview"/"production" rows if
// they are still empty — deploy environments are project-scoped now.
func (q *Queries) EnsureDefaultEnvironments(ctx context.Context, companyID int32) error {
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Check-then-insert instead of ON CONFLICT: NULL project_id rows are
		// mutually distinct under the composite unique index (SQL NULL
		// semantics), so an upsert would never see a conflict and duplicate
		// the platform env on every call. The partial unique index
		// idx_env_platform_name is the backstop for concurrent seeding.
		var existing Environment
		err := tx.Where("company_id = ? AND project_id IS NULL AND name = ?", companyID, DefaultEnvironmentName).
			First(&existing).Error
		if err != nil {
			if err != gorm.ErrRecordNotFound {
				return err
			}
			env := Environment{
				CompanyID: companyID,
				Name:      DefaultEnvironmentName,
				Builtin:   true,
				IsDefault: true,
			}
			if err := tx.Create(&env).Error; err != nil {
				return err
			}
		}
		// Legacy cleanup: drop the old company-level preview/production seeds
		// when they carry no entries (an entry means the user adopted them —
		// leave those alone rather than destroy data).
		var stale []Environment
		if err := tx.Where("company_id = ? AND project_id IS NULL AND builtin AND NOT is_default", companyID).
			Find(&stale).Error; err != nil {
			return err
		}
		for _, s := range stale {
			var n int64
			if err := tx.Model(&EnvironmentSecret{}).Where("environment_id = ?", s.ID).Count(&n).Error; err != nil {
				return err
			}
			if n == 0 {
				if err := tx.Delete(&Environment{}, s.ID).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// ListEnvironments returns a company's company-level environments (i.e. the
// platform env), seeding on first call. Project deploy environments live in
// ListProjectEnvironments.
func (q *Queries) ListEnvironments(ctx context.Context, companyID int32) ([]Environment, error) {
	if err := q.EnsureDefaultEnvironments(ctx, companyID); err != nil {
		return nil, err
	}
	var envs []Environment
	err := q.db.WithContext(ctx).
		Where("company_id = ? AND project_id IS NULL", companyID).
		Order("builtin desc, name asc").
		Find(&envs).Error
	return envs, err
}

// ListProjectEnvironments returns a project's deploy environments by name.
// Projects start with none; the user creates them explicitly.
func (q *Queries) ListProjectEnvironments(ctx context.Context, projectID int32) ([]Environment, error) {
	var envs []Environment
	err := q.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("name asc").
		Find(&envs).Error
	return envs, err
}

// CreateProjectEnvironment adds a deploy environment to a project.
func (q *Queries) CreateProjectEnvironment(ctx context.Context, companyID, projectID int32, name string) (Environment, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Environment{}, fmt.Errorf("environment name is required")
	}
	env := Environment{CompanyID: companyID, ProjectID: &projectID, Name: name}
	err := q.db.WithContext(ctx).Create(&env).Error
	return env, err
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

// DeleteEnvironment removes a user-defined environment and its secrets.
func (q *Queries) DeleteEnvironment(ctx context.Context, id int32) error {
	env, err := q.GetEnvironment(ctx, id)
	if err != nil {
		return err
	}
	if env.Builtin {
		return fmt.Errorf("builtin environments cannot be deleted")
	}
	return q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// SQLite (glebarez) doesn't always enforce FK cascade on
		// AutoMigrate-created schemas; delete dependents explicitly.
		if err := tx.Where("environment_id = ?", id).Delete(&EnvironmentSecret{}).Error; err != nil {
			return err
		}
		if err := tx.Where("environment_id = ?", id).Delete(&EnvironmentConnector{}).Error; err != nil {
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

// UpsertEnvironmentSecret creates or replaces a named entry (secret or
// variable) in an environment. The plaintext is sealed here, at the point of
// write, under the calling user's DEK — returns secrets.ErrLocked if their
// vault is locked. kind defaults to "secret".
func (q *Queries) UpsertEnvironmentSecret(ctx context.Context, environmentID int32, userID int32, name, value, kind string) (EnvironmentSecret, error) {
	if err := ValidateEnvVarName(name); err != nil {
		return EnvironmentSecret{}, err
	}
	switch kind {
	case "":
		kind = EnvEntrySecret
	case EnvEntrySecret, EnvEntryVariable:
	default:
		return EnvironmentSecret{}, fmt.Errorf("invalid kind %q: must be %q or %q", kind, EnvEntrySecret, EnvEntryVariable)
	}
	sealed, err := secrets.Default().EncryptForUser(userID, value)
	if err != nil {
		return EnvironmentSecret{}, err
	}
	row := EnvironmentSecret{
		EnvironmentID:  environmentID,
		Name:           name,
		Kind:           kind,
		ValueEncrypted: sealed,
		UserID:         &userID,
	}
	err = q.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "environment_id"}, {Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"value", "kind", "user_id", "updated_at"}),
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

// DefaultEnvironmentSecrets returns the company's default environment
// ("headcount1 cloud" — the one every task runs in) plus its secret rows
// (values still sealed — decrypt at the point of use). Only this
// environment's secrets are injected into agent shells; the other
// environments describe external deploy targets and are not exposed here.
func (q *Queries) DefaultEnvironmentSecrets(ctx context.Context, companyID int32) (Environment, []EnvironmentSecret, error) {
	env, err := q.GetDefaultEnvironment(ctx, companyID)
	if err != nil {
		return Environment{}, nil, err
	}
	rows, err := q.ListEnvironmentSecrets(ctx, env.ID)
	return env, rows, err
}
