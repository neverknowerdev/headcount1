package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"gorm.io/gorm"
)

type MCPToolStatRepository struct{ db *gorm.DB }

func NewMCPToolStatRepository(db *gorm.DB) *MCPToolStatRepository {
	return &MCPToolStatRepository{db: db}
}
func (q *MCPToolStatRepository) IncrementMCPToolCallCount(ctx context.Context, serverID int32, toolName string) error {
	return q.db.WithContext(ctx).Exec(`INSERT INTO mcp_tool_stats (mcp_server_id, tool_name, call_count) VALUES (?, ?, 1)
		 ON CONFLICT (mcp_server_id, tool_name) DO UPDATE SET call_count = mcp_tool_stats.call_count + 1`, serverID, toolName).Error
}
func (q *MCPToolStatRepository) GetMCPToolCallCounts(ctx context.Context, serverID int32) (map[string]int64, error) {
	var stats []MCPToolStat
	if err := q.db.WithContext(ctx).Where("mcp_server_id = ?", serverID).Find(&stats).Error; err != nil {
		return nil, err
	}
	result := make(map[string]int64, len(stats))
	for _, s := range stats {
		result[s.ToolName] = s.CallCount
	}
	return result, nil
}
