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
	"agent-orchestrator/pkg/filesystem"
)

func (api *API) ListComments(w http.ResponseWriter, r *http.Request) {
	taskID, err := strconv.Atoi(r.URL.Query().Get("task_id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "task_id is required")
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
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}
	p := db.Comment{
		TaskID:     req.TaskID,
		AuthorType: req.AuthorType,
		Content:    req.Content,
		AuthorID:   req.AuthorID,
	}

	comment, err := api.q.CreateComment(r.Context(), p)
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	api.hub.BroadcastEvent("comment_created", comment)

	var task db.Task
	if api.db.First(&task, req.TaskID).Error == nil {
		var comp db.Company
		if api.db.First(&comp, task.CompanyID).Error == nil {
			var allComments []db.Comment
			api.db.Where("task_id = ?", req.TaskID).Order("created_at asc").Find(&allComments)
			settings := LoadSettings()
			filesystem.NewManager(settings.BasePath).SaveTaskComments(comp, req.TaskID, allComments)
		}
	}

	if req.RunAgent {
		task, err := api.q.GetTask(r.Context(), req.TaskID)
		if err == nil && task.Status != "backlog" {
			go api.engine.ProcessTask(context.Background(), req.TaskID)
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

	file, handler, err := r.FormFile("file")
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "Error retrieving file")
		return
	}
	defer file.Close()

	uploadDir := "./uploads"
	if err := os.MkdirAll(uploadDir, os.ModePerm); err != nil {
		api.respondError(w, http.StatusInternalServerError, "Unable to create upload directory")
		return
	}

	filePath := filepath.Join(uploadDir, strconv.FormatInt(time.Now().UnixNano(), 10)+"_"+handler.Filename)
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

	// Write attachment metadata to filesystem
	settings := LoadSettings()
	storage := filesystem.NewStorage(settings.BasePath)
	companyShortName, err := storage.GetCompanyShortNameForTask(int32(taskID))
	if err == nil {
		if err := storage.WriteAttachment(attachment, companyShortName); err != nil {
			log.Printf("Warning: failed to write attachment metadata: %v", err)
		}
	}

	api.respondJSON(w, http.StatusCreated, attachment)
}
