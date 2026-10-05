package tools

import (
	"context"
	"encoding/json"
	"testing"

	"agent-orchestrator/engine/aicli"
	"github.com/stretchr/testify/require"
)

func TestReportStatusPassesCallingMessageID(t *testing.T) {
	var got int64
	tool := NewReportStatus(func(_ context.Context, status string, messageID int64) error {
		require.Equal(t, "implementing", status)
		got = messageID
		return nil
	})
	ctx := aicli.WithToolCallMessageID(context.Background(), 42)
	_, err := tool.Execute(ctx, json.RawMessage(`{"status":"implementing"}`))
	require.NoError(t, err)
	require.Equal(t, int64(42), got)
}
