package server

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
	"agent-orchestrator/db"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/skills"
	"agent-orchestrator/engine"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all for MVP
	},
}

type Server struct {
	db     *gorm.DB
	q      db.Querier
	hub    *eventhub.Hub
	engine *engine.ForgeEngine
}

func NewServer(database *gorm.DB, eng *engine.ForgeEngine) *Server {
	return &Server{
		db:     database,
		q:      db.New(database),
		engine: eng,
	}
}

func (s *Server) SetHub(h *eventhub.Hub) { s.hub = h }

func (s *Server) Mount(r chi.Router) {
	go s.hub.Run()

	r.Get("/ws", s.serveWs)

	r.Route("/companies", func(r chi.Router) {
		r.Get("/", s.listCompanies)
		r.Post("/", s.createCompany)
	})

	r.Route("/projects", func(r chi.Router) {
		r.Get("/", s.listProjects)
		r.Post("/", s.createProject)
	})

	r.Route("/tasks", func(r chi.Router) {
		r.Get("/", s.listTasks)
		r.Post("/", s.createTask)
		r.Get("/{id}", s.getTask)
		r.Put("/{id}/status", s.updateTaskStatus)
	})

	r.Route("/agents", func(r chi.Router) {
		r.Get("/", s.listAgents)
		r.Post("/", s.createAgent)
	})

	r.Route("/comments", func(r chi.Router) {
		r.Get("/", s.listComments)
		r.Post("/", s.createComment)
	})

	r.Post("/attachments", s.uploadAttachment)
	r.Post("/skills/import", s.importSkill)
}

func (s *Server) serveWs(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println(err)
		return
	}
	client := &eventhub.Client{Hub: s.hub, Conn: conn, Send: make(chan []byte, 256)}
	client.Hub.Register <- client

	go client.WritePump()
	go client.ReadPump()
}

func respondJSON(w http.ResponseWriter, status int, payload interface{}) {
	response, err := json.Marshal(payload)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(response)
}

func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]string{"error": message})
}

func (s *Server) listCompanies(w http.ResponseWriter, r *http.Request) {
	companies, err := s.q.ListCompanies(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, companies)
}

func (s *Server) createCompany(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}
	comp, err := s.q.CreateCompany(r.Context(), req.Name)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusCreated, comp)
}

func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	compID, err := strconv.Atoi(r.URL.Query().Get("company_id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "company_id is required")
		return
	}
	projects, err := s.q.ListProjectsByCompany(r.Context(), int32(compID))
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, projects)
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CompanyID   int32  `json:"company_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}
	p := db.Project{
		CompanyID: req.CompanyID,
		Name:      req.Name,
		Description: req.Description,
	}

	proj, err := s.q.CreateProject(r.Context(), p)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusCreated, proj)
}

func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	compID, err := strconv.Atoi(r.URL.Query().Get("company_id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "company_id is required")
		return
	}
	agents, err := s.q.ListAgentsByCompany(r.Context(), int32(compID))
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, agents)
}

func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CompanyID    int32  `json:"company_id"`
		Name         string `json:"name"`
		Description  string `json:"description"`
		SystemPrompt string `json:"system_prompt"`
		Model        string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}
	p := db.Agent{
		CompanyID:    req.CompanyID,
		Name:         req.Name,
		SystemPrompt: req.SystemPrompt,
		Description:  req.Description,
		Model:        req.Model,
	}

	agent, err := s.q.CreateAgent(r.Context(), p)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusCreated, agent)
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	projID, err := strconv.Atoi(r.URL.Query().Get("project_id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "project_id is required")
		return
	}
	tasks, err := s.q.ListTasksByProject(r.Context(), int32(projID))
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, tasks)
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ProjectID   int32  `json:"project_id"`
		AgentID     *int32 `json:"agent_id"`
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}
	p := db.Task{
		ProjectID: req.ProjectID,
		Title:     req.Title,
		Status:    "backlog",
		AgentID:   req.AgentID,
		Description: req.Description,
	}

	task, err := s.q.CreateTask(r.Context(), p)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.hub.BroadcastEvent("task_created", task)
	respondJSON(w, http.StatusCreated, task)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	task, err := s.q.GetTask(r.Context(), int32(id))
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, task)
}

func (s *Server) updateTaskStatus(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	task, err := s.q.UpdateTaskStatus(r.Context(), int32(id), req.Status)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.hub.BroadcastEvent("task_updated", task)

	go s.engine.ProcessTask(r.Context(), int32(id))

	respondJSON(w, http.StatusOK, task)
}

func (s *Server) listComments(w http.ResponseWriter, r *http.Request) {
	taskID, err := strconv.Atoi(r.URL.Query().Get("task_id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "task_id is required")
		return
	}
	comments, err := s.q.ListCommentsByTask(r.Context(), int32(taskID))
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, comments)
}

func (s *Server) createComment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID     int32  `json:"task_id"`
		AuthorType string `json:"author_type"`
		AuthorID   *int32 `json:"author_id"`
		Content    string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}
	p := db.Comment{
		TaskID:     req.TaskID,
		AuthorType: req.AuthorType,
		Content:    req.Content,
		AuthorID:   req.AuthorID,
	}

	comment, err := s.q.CreateComment(r.Context(), p)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.hub.BroadcastEvent("comment_created", comment)
	respondJSON(w, http.StatusCreated, comment)
}

func (s *Server) importSkill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CompanyID int32  `json:"company_id"`
		Name      string `json:"name"`
		SourceURL string `json:"source_url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	skillMgr := skills.NewManager(s.db)
	skill, err := skillMgr.ImportSkill(r.Context(), req.CompanyID, req.Name, req.SourceURL)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusCreated, skill)
}

func (s *Server) uploadAttachment(w http.ResponseWriter, r *http.Request) {
	err := r.ParseMultipartForm(10 << 20)
	if err != nil {
		respondError(w, http.StatusBadRequest, "File too large")
		return
	}

	taskID, err := strconv.Atoi(r.FormValue("task_id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "task_id is required")
		return
	}

	file, handler, err := r.FormFile("file")
	if err != nil {
		respondError(w, http.StatusBadRequest, "Error retrieving file")
		return
	}
	defer file.Close()

	uploadDir := "./uploads"
	if err := os.MkdirAll(uploadDir, os.ModePerm); err != nil {
		respondError(w, http.StatusInternalServerError, "Unable to create upload directory")
		return
	}

	filePath := filepath.Join(uploadDir, strconv.FormatInt(time.Now().UnixNano(), 10)+"_"+handler.Filename)
	dst, err := os.Create(filePath)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Unable to save file")
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, file); err != nil {
		respondError(w, http.StatusInternalServerError, "Unable to save file")
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

	attachment, err := s.q.CreateAttachment(r.Context(), p)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	respondJSON(w, http.StatusCreated, attachment)
}
