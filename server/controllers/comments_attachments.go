package endpoints

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"agent-orchestrator/db"
)

func (api *API) ListComments(w http.ResponseWriter, r *http.Request) {
	taskID, err := strconv.Atoi(r.URL.Query().Get("task_id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "task_id is required")
		return
	}
	if _, err := api.authorizeTask(r, int32(taskID)); err != nil {
		api.respondError(w, http.StatusNotFound, "task not found")
		return
	}
	comments, err := api.q.ListCommentsByTask(r.Context(), int32(taskID))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.respondJSON(w, http.StatusOK, comments)
}

func (api *API) CreateComment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID     int32  `json:"task_id"`
		AuthorType string `json:"author_type"`
		AuthorID   *int32 `json:"author_id"`
		Content    string `json:"content"`
		RunAgent   bool   `json:"run_agent"`
		// ReplyToID names the question this comment answers, when a task asked
		// more than one.
		ReplyToID *int32 `json:"reply_to_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}
	authTask, err := api.authorizeTask(r, req.TaskID)
	if err != nil {
		api.respondError(w, http.StatusNotFound, "task not found")
		return
	}
	if req.ReplyToID != nil {
		// A reply must answer a question asked in this task's own tree.
		question, err := api.q.GetComment(r.Context(), *req.ReplyToID)
		if err != nil || question.CommentType != "ask_user" {
			api.respondError(w, http.StatusNotFound, "question not found")
			return
		}
		asked, err := api.q.GetTask(r.Context(), question.TaskID)
		if err != nil || asked.RootTaskID != authTask.RootTaskID {
			api.respondError(w, http.StatusNotFound, "question not found")
			return
		}
	}
	p := db.Comment{
		TaskID:     req.TaskID,
		AuthorType: req.AuthorType,
		Content:    req.Content,
		AuthorID:   req.AuthorID,
		ReplyToID:  req.ReplyToID,
	}

	comment, err := api.q.CreateComment(r.Context(), p)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.hub.BroadcastEventForCompany(authTask.CompanyID, "comment_created", comment)
	if req.AuthorType == "human" {
		// The comment may answer a question a task in this tree is waiting on.
		// It is already stored, so a failure here is not the request's: the
		// workflow looks for the answer again on its own.
		if err := api.engine.HandleHumanReply(r.Context(), req.TaskID); err != nil {
			log.Printf("human reply not yet picked up for task %d: %v", req.TaskID, err)
		}
	}

	if req.RunAgent {
		task, err := api.q.GetTask(r.Context(), req.TaskID)
		if err == nil && task.Status != "backlog" {
			go api.engine.RerunTask(context.Background(), req.TaskID)
		}
	}

	api.respondJSON(w, http.StatusCreated, comment)
}

func (api *API) UploadAttachment(w http.ResponseWriter, r *http.Request) {
	err := r.ParseMultipartForm(10 << 20)
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "File too large")
		return
	}

	taskID, err := strconv.Atoi(r.FormValue("task_id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "task_id is required")
		return
	}

	if _, err := api.authorizeTask(r, int32(taskID)); err != nil {
		api.respondError(w, http.StatusNotFound, "task not found")
		return
	}

	file, handler, err := r.FormFile("file")
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "Error retrieving file")
		return
	}
	defer file.Close()

	settings := LoadSettings()
	uploadDir := filepath.Join(settings.BasePath, "uploads", strconv.Itoa(taskID))
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		api.respondError(w, http.StatusInternalServerError, "Unable to create upload directory")
		return
	}

	filePath := filepath.Join(uploadDir, strconv.FormatInt(time.Now().UnixNano(), 10)+"_"+filepath.Base(handler.Filename))
	dst, err := os.Create(filePath)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, "Unable to save file")
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		api.respondError(w, http.StatusInternalServerError, "Unable to save file")
		return
	}

	p := db.Attachment{
		TaskID:   int32(taskID),
		Filename: handler.Filename,
		FilePath: filePath,
	}
	if mimeType := handler.Header.Get("Content-Type"); mimeType != "" {
		p.MimeType = mimeType
	}

	attachment, err := api.q.CreateAttachment(r.Context(), p)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	api.respondJSON(w, http.StatusCreated, attachment)
}
