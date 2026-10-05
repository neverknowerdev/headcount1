package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFinishTaskPassesPullRequestMetadataToEngine(t *testing.T) {
	var captured FinishTaskResult
	tool := NewFinishTask(false, func(_ context.Context, result FinishTaskResult) error {
		captured = result
		return nil
	})
	_, err := tool.Execute(t.Context(), json.RawMessage(`{
		"task_status":"in-review",
		"finish_status":"OAuth completed",
		"result_details":"Implemented and tested.",
		"pull_request_title":"Improve GitHub OAuth",
		"pull_request_description":"Separates persistence and verifies user isolation."
	}`))
	require.NoError(t, err)
	require.Equal(t, "in-review", captured.Status)
	require.Equal(t, "Improve GitHub OAuth", captured.PullRequestTitle)
	require.Equal(t, "Separates persistence and verifies user isolation.", captured.PullRequestDescription)
}
