package models

import "time"

// Environment scopes runtime secrets or deployment variables. Company-level
// environments may be injected into agent runs; project-level environments
// are synchronized to deployment providers.
type Environment struct {
	ID        int32     `json:"id" gorm:"primaryKey"`
	CompanyID int32     `json:"company_id" gorm:"not null;index;uniqueIndex:idx_env_scope_name;uniqueIndex:idx_env_platform_name,where:project_id IS NULL"`
	Company   Company   `json:"-" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	ProjectID *int32    `json:"project_id" gorm:"index;uniqueIndex:idx_env_scope_name"`
	Project   *Project  `json:"-" gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE;"`
	Name      string    `json:"name" gorm:"not null;uniqueIndex:idx_env_scope_name;uniqueIndex:idx_env_platform_name,where:project_id IS NULL"`
	IsDefault bool      `json:"is_default" gorm:"not null;default:false"`
	Builtin   bool      `json:"builtin" gorm:"not null;default:false"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// EnvironmentSecret stores one encrypted secret or variable.
type EnvironmentSecret struct {
	ID             int32       `json:"id" gorm:"primaryKey"`
	EnvironmentID  int32       `json:"environment_id" gorm:"not null;index;uniqueIndex:idx_envsecret_env_name"`
	Environment    Environment `json:"-" gorm:"foreignKey:EnvironmentID;constraint:OnDelete:CASCADE;"`
	Name           string      `json:"name" gorm:"not null;uniqueIndex:idx_envsecret_env_name"`
	Kind           string      `json:"kind" gorm:"not null;default:'secret'"`
	ValueEncrypted string      `json:"-" gorm:"column:value;type:text;serializer:sealed"`
	HasValue       bool        `json:"has_value" gorm:"-"`
	UserID         *int32      `json:"user_id" gorm:"index"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

// EnvironmentConnector links a project environment to a deployment target.
type EnvironmentConnector struct {
	ID                int32       `json:"id" gorm:"primaryKey"`
	EnvironmentID     int32       `json:"environment_id" gorm:"not null;index"`
	Environment       Environment `json:"-" gorm:"foreignKey:EnvironmentID;constraint:OnDelete:CASCADE;"`
	Provider          string      `json:"provider" gorm:"not null"`
	TargetProject     string      `json:"target_project" gorm:"not null"`
	TargetEnvironment string      `json:"target_environment" gorm:"not null"`
	TeamID            string      `json:"team_id"`
	ApiTokenEncrypted string      `json:"-" gorm:"column:api_token;type:text;serializer:sealed"`
	HasToken          bool        `json:"has_token" gorm:"-"`
	UserID            *int32      `json:"user_id" gorm:"index"`
	LastSyncAt        *time.Time  `json:"last_sync_at"`
	LastSyncStatus    string      `json:"last_sync_status"`
	LastSyncError     string      `json:"last_sync_error" gorm:"type:text"`
	CreatedAt         time.Time   `json:"created_at"`
	UpdatedAt         time.Time   `json:"updated_at"`
}

const (
	DefaultEnvironmentName = "headcount1 cloud"
	EnvEntrySecret         = "secret"
	EnvEntryVariable       = "variable"
	ConnectorVercel        = "vercel"
	ConnectorGitHub        = "github"
)
