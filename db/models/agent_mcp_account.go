package models

type AgentMCPAccount struct {
	AgentID      int32 `json:"agent_id" gorm:"primaryKey"`
	MCPAccountID int32 `json:"mcp_account_id" gorm:"primaryKey"`
	Enabled      bool  `json:"enabled" gorm:"not null;default:true"`
}
