package db_test

import (
	"context"
	"testing"

	"agent-orchestrator/db"

	"github.com/stretchr/testify/require"
)

func TestRunEventDurableMessageAnswerIsRoutedAndIdempotent(t *testing.T) {
	database := setupModelGroupTestDB(t)
	q := db.New(database)
	ctx := context.Background()

	request, err := q.EnqueueRoutedEvent(ctx, 7, 11, 22, db.RunEventTypeSessionMessage, `{"schema_version":1,"message":"choose A or B"}`, "message-1")
	require.NoError(t, err)
	require.NotZero(t, request.ID)
	require.NotNil(t, request.SourceRunID)
	require.Equal(t, int32(11), *request.SourceRunID)
	require.Equal(t, int32(22), *request.TargetRunID)

	pending, err := q.ListUnconsumedEventsForTarget(ctx, 22, db.RunEventTypeSessionMessage)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	answer, err := q.AnswerPendingMessage(ctx, 22, request.ID, "Choose A", "answer-1")
	require.NoError(t, err)
	require.Equal(t, request.ID, *answer.ReplyToEventID)

	answerAgain, err := q.AnswerPendingMessage(ctx, 22, request.ID, "Choose A", "answer-1")
	require.NoError(t, err)
	require.Equal(t, answer.ID, answerAgain.ID)

	pending, err = q.ListUnconsumedEventsForTarget(ctx, 22, db.RunEventTypeSessionMessage)
	require.NoError(t, err)
	require.Empty(t, pending)
	found, err := q.FindAnswerForMessage(ctx, request.ID)
	require.NoError(t, err)
	require.Equal(t, answer.ID, found.ID)
}

func TestRunEventDurableMessageRejectsWrongTarget(t *testing.T) {
	database := setupModelGroupTestDB(t)
	q := db.New(database)
	ctx := context.Background()
	request, err := q.EnqueueRoutedEvent(ctx, 7, 11, 22, db.RunEventTypeSessionMessage, "question", "message-wrong-target")
	require.NoError(t, err)
	_, err = q.AnswerPendingMessage(ctx, 99, request.ID, "no", "answer-wrong-target")
	require.Error(t, err)
	answer, findErr := q.FindAnswerForMessage(ctx, request.ID)
	require.Error(t, findErr)
	require.Zero(t, answer.ID)
}
