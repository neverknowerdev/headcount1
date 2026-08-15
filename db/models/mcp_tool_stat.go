package models

type MCPToolStat struct {
	ID          int32  `json:"id" gorm:"primaryKey;autoIncrement"`
	MCPServerID int32  `json:"mcp_server_id" gorm:"not null;uniqueIndex:idx_mcp_tool_stat"`
	ToolName    string `json:"tool_name" gorm:"not null;uniqueIndex:idx_mcp_tool_stat"`
	CallCount   int64  `json:"call_count" gorm:"not null;default:0"`
}
