package models

import "time"

type Project struct {
	ID                   int32     `json:"id" gorm:"primaryKey"`
	CompanyID            int32     `json:"company_id" gorm:"not null"`
	Company              Company   `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	Name                 string    `json:"name" gorm:"not null"`
	Description          string    `json:"description"`
	WorkspaceFolder      string    `json:"workspace_folder"`
	RepositoryUrl        string    `json:"repository_url"`
	GitHubRepositoryID   int64     `json:"github_repository_id" gorm:"index"`
	GitHubInstallationID int64     `json:"github_installation_id" gorm:"index"`
	GitHubDefaultBranch  string    `json:"github_default_branch"`
	IsExternal           bool      `json:"is_external" gorm:"not null;default:false"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}
