package models

type AgentMCPToolFilter struct {
	AgentID     int32  `json:"agent_id" gorm:"primaryKey"`
	MCPServerID int32  `json:"mcp_server_id" gorm:"primaryKey"`
	ToolName    string `json:"tool_name" gorm:"primaryKey"`
	Enabled     bool   `json:"enabled" gorm:"not null;default:true"`
}
