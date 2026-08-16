package models

type AgentMCPServer struct {
	AgentID     int32 `json:"agent_id" gorm:"primaryKey"`
	MCPServerID int32 `json:"mcp_server_id" gorm:"primaryKey"`
	Enabled     bool  `json:"enabled" gorm:"not null;default:true"`
}
