package models

import "time"

const (
	MCPServerNameGitHub  = "github"
	MCPAuthTypeGitHubApp = "github-app"
	MCPTransportBuiltin  = "builtin"
)

func (server MCPServer) IsGitHub() bool {
	return server.Builtin && server.Name == MCPServerNameGitHub && server.AuthType == MCPAuthTypeGitHubApp
}

type MCPServer struct {
	ID            int32        `json:"id" gorm:"primaryKey"`
	Name          string       `json:"name" gorm:"not null;uniqueIndex"`
	OwnerUserID   *int32       `json:"owner_user_id" gorm:"index"`
	DisplayName   string       `json:"display_name"`
	Description   string       `json:"description"`
	Transport     string       `json:"transport" gorm:"not null"`
	Command       string       `json:"command"`
	Args          string       `json:"args" gorm:"type:text"`
	URL           string       `json:"url"`
	Headers       string       `json:"headers" gorm:"type:text"`
	AuthType      string       `json:"auth_type"`
	AuthToken     string       `json:"-" gorm:"-"`
	AuthEnvVar    string       `json:"auth_env_var"`
	ToolsCache    string       `json:"tools_cache" gorm:"type:text"`
	LastError     string       `json:"last_error" gorm:"type:text"`
	InitStatus    string       `json:"init_status" gorm:"default:''"`
	DepsInstalled bool         `json:"deps_installed" gorm:"-"`
	Deps          string       `json:"deps" gorm:"type:text"`
	Enabled       bool         `json:"enabled" gorm:"not null;default:true"`
	Builtin       bool         `json:"builtin" gorm:"not null;default:false"`
	WorkDir       string       `json:"work_dir"`
	ProjectID     *int32       `json:"project_id" gorm:"index"`
	Project       *Project     `json:"project,omitempty" gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE;"`
	Accounts      []MCPAccount `json:"accounts,omitempty" gorm:"foreignKey:MCPServerID"`
	Agents        []Agent      `json:"agents,omitempty" gorm:"many2many:agent_mcp_servers;"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
}
