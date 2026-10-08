package endpoints

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"agent-orchestrator/db"
	"agent-orchestrator/db/migrations"
	"agent-orchestrator/engine/enginetest"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/pkg/authctx"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// The engine may fail to pick a human's answer up at the moment it is posted.
// The comment is already stored and the workflow looks for it again on its
// own, so the request still succeeds: a failure here would invite the human to
// answer twice.
func TestCreateCommentKeepsHumanReplyDurableWhenResumeFails(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))

	user := db.User{Email: "human-reply@example.test"}
	require.NoError(t, database.Create(&user).Error)
	company := db.Company{Name: "Human Reply API Co", ShortName: "HRA", UserID: &user.ID}
	require.NoError(t, database.Create(&company).Error)
	sprint := db.Sprint{CompanyID: company.ID, Name: "Reply sprint"}
	require.NoError(t, database.Create(&sprint).Error)
	task := db.Task{CompanyID: company.ID, SprintID: sprint.ID, Title: "answer a question", Status: db.TaskStatusBlocked}
	require.NoError(t, database.Create(&task).Error)

	engineDouble := &enginetest.Recorder{HumanReplyErr: errors.New("temporary failure")}
	api := NewAPI(database, engineDouble, eventhub.NewHub())
	req := httptest.NewRequest(http.MethodPost, "/api/comments", bytes.NewBufferString(`{"task_id":1,"author_type":"human","content":"approved"}`))
	req = req.WithContext(authctx.WithUser(req.Context(), user))
	res := httptest.NewRecorder()
	api.CreateComment(res, req)

	require.Equal(t, http.StatusCreated, res.Code)
	var comments []db.Comment
	require.NoError(t, database.Where("task_id = ?", task.ID).Find(&comments).Error)
	require.Len(t, comments, 1)
	require.Equal(t, "approved", comments[0].Content)
	require.Equal(t, []string{"human-reply 1"}, engineDouble.Calls())
}

// A reply can name the question it answers, but only a question asked in the
// same task tree.
func TestCreateCommentReplyMustAnswerAQuestionOfItsTree(t *testing.T) {
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, _ := database.DB()
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))

	user := db.User{Email: "reply-to@example.test"}
	require.NoError(t, database.Create(&user).Error)
	company := db.Company{Name: "Reply Co", ShortName: "RPL", UserID: &user.ID}
	require.NoError(t, database.Create(&company).Error)
	sprint := db.Sprint{CompanyID: company.ID, Name: "Reply sprint"}
	require.NoError(t, database.Create(&sprint).Error)
	q := db.New(database)
	ctx := context.Background()
	root, err := q.CreateTask(ctx, db.Task{CompanyID: company.ID, SprintID: sprint.ID, Title: "root", Status: db.TaskStatusBlocked})
	require.NoError(t, err)
	other, err := q.CreateTask(ctx, db.Task{CompanyID: company.ID, SprintID: sprint.ID, Title: "unrelated", Status: db.TaskStatusBlocked})
	require.NoError(t, err)
	question, err := q.CreateComment(ctx, db.Comment{TaskID: root.ID, AuthorType: "agent", CommentType: "ask_user", Content: "Which region?"})
	require.NoError(t, err)
	remark, err := q.CreateComment(ctx, db.Comment{TaskID: root.ID, AuthorType: "agent", Content: "Working on it."})
	require.NoError(t, err)
	foreign, err := q.CreateComment(ctx, db.Comment{TaskID: other.ID, AuthorType: "agent", CommentType: "ask_user", Content: "Which colour?"})
	require.NoError(t, err)

	api := NewAPI(database, &enginetest.Recorder{}, eventhub.NewHub())
	post := func(replyTo int32) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"task_id":%d,"author_type":"human","content":"eu-west","reply_to_id":%d}`, root.ID, replyTo)
		req := httptest.NewRequest(http.MethodPost, "/api/comments", bytes.NewBufferString(body))
		req = req.WithContext(authctx.WithUser(req.Context(), user))
		res := httptest.NewRecorder()
		api.CreateComment(res, req)
		return res
	}

	require.Equal(t, http.StatusNotFound, post(remark.ID).Code, "a comment that is not a question cannot be answered")
	require.Equal(t, http.StatusNotFound, post(foreign.ID).Code, "a question of another task tree cannot be answered here")
	require.Equal(t, http.StatusNotFound, post(9999).Code)

	require.Equal(t, http.StatusCreated, post(question.ID).Code)
	var reply db.Comment
	require.NoError(t, database.Where("task_id = ? AND author_type = ?", root.ID, "human").First(&reply).Error)
	require.NotNil(t, reply.ReplyToID)
	require.Equal(t, question.ID, *reply.ReplyToID)
}
