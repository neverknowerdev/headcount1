package endpoints

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/db/migrations"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/pkg/authctx"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// humanReplyErrorEngine models a transient resume failure after the human
// comment has already been committed. The API contract is that the durable
// answer remains a successful request and can be retried by the waiter.
type humanReplyErrorEngine struct{}

func (humanReplyErrorEngine) ProcessTask(context.Context, int32) error { return nil }
func (humanReplyErrorEngine) RerunTask(context.Context, int32) error   { return nil }
func (humanReplyErrorEngine) StopRun(context.Context, int32)           {}
func (humanReplyErrorEngine) HandleHumanReply(context.Context, int32) error {
	return errors.New("temporary resume failure")
}

func TestCreateCommentKeepsHumanReplyDurableWhenResumeFails(t *testing.T) {
	database, err := gorm.Open(sqlite.Open("file:create-comment-human-reply?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))

	user := db.User{Email: "human-reply@example.test"}
	require.NoError(t, database.Create(&user).Error)
	company := db.Company{Name: "Human Reply API Co", ShortName: "HRA", UserID: &user.ID}
	require.NoError(t, database.Create(&company).Error)
	sprint := db.Sprint{CompanyID: company.ID, Name: "Reply sprint"}
	require.NoError(t, database.Create(&sprint).Error)
	task := db.Task{CompanyID: company.ID, SprintID: sprint.ID, Title: "answer a question", Status: db.TaskStatusBlocked}
	require.NoError(t, database.Create(&task).Error)

	api := NewAPI(database, humanReplyErrorEngine{}, eventhub.NewHub())
	req := httptest.NewRequest(http.MethodPost, "/api/comments", bytes.NewBufferString(`{"task_id":1,"author_type":"human","content":"approved"}`))
	req = req.WithContext(authctx.WithUser(req.Context(), user))
	res := httptest.NewRecorder()
	api.CreateComment(res, req)

	require.Equal(t, http.StatusCreated, res.Code)
	var comments []db.Comment
	require.NoError(t, database.Where("task_id = ?", task.ID).Find(&comments).Error)
	require.Len(t, comments, 1)
	require.Equal(t, "approved", comments[0].Content)
}
