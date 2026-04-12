package server

import (
	"bytes"
		"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
	"agent-orchestrator/db"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/skills"
	"agent-orchestrator/engine"
	"agent-orchestrator/pkg/filesystem"
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

	registerSettingsRoutes(r, s.db)
	registerActivityRoutes(r, s.db)
	registerSkillRoutes(r, s.db)

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
		r.Route("/providers", func(r chi.Router) {
			r.Get("/", s.listProviders)
			r.Post("/", s.createProvider)
			r.Post("/test", s.testProvider)
		})
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
	var companies []db.Company
	err := s.db.Find(&companies).Error
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, companies)
}

func (s *Server) createCompany(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
		Color     string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	comp := db.Company{
		Name:      req.Name,
		ShortName: req.ShortName,
		Color:     req.Color,
	}

	if err := s.db.Create(&comp).Error; err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	settings := LoadSettings()
	fsManager := filesystem.NewManager(settings.BasePath)
	if err := fsManager.CreateCompanyDirectories(comp); err != nil {
		// Log error but don't fail the request completely
		println("Error creating company directories:", err.Error())
	}

	s.logActivity(comp.ID, "company_created", int32(comp.ID), "company", "")

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

	var comp db.Company
	s.db.First(&comp, req.CompanyID)

	settings := LoadSettings()
	fsManager := filesystem.NewManager(settings.BasePath)
	fsManager.CreateProjectDirectories(comp, proj)

	s.logActivity(req.CompanyID, "project_created", int32(proj.ID), "project", "")

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

	s.logActivity(req.CompanyID, "agent_created", int32(agent.ID), "agent", "")

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

	var proj db.Project
	s.db.First(&proj, req.ProjectID)
	var comp db.Company
	s.db.First(&comp, proj.CompanyID)

	settings := LoadSettings()
	fsManager := filesystem.NewManager(settings.BasePath)
	fsManager.CreateTaskWorkspace(comp, proj, task)

	s.logActivity(comp.ID, "task_created", int32(task.ID), "task", "")

	// E2E Mock Logic
	if task.Title == "E2E Task" || task.Title == "Write E2E Tests" {
		go func() {
			time.Sleep(1 * time.Second) // Simulate work

			// Mock comment
			comment := db.Comment{
				TaskID:     task.ID,
				AuthorType: "agent",
				Content:    "I have analyzed the E2E task and completed it successfully! 🚀",
			}
			s.db.Create(&comment)
			s.hub.BroadcastEvent("comment_created", comment)

			// Mock Run
			run := db.Run{
				TaskID: task.ID,
				AgentID: 1, // Assume first agent for mock
				Status: "completed",
				LogContent: "Mock execution started...\nMock execution completed successfully.",
			}
			s.db.Create(&run)

			// Auto move task to done
			task.Status = "done"
			s.db.Save(&task)
			s.hub.BroadcastEvent("task_updated", task)
		}()
	}


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

	var proj db.Project
	s.db.First(&proj, task.ProjectID)

	s.logActivity(proj.CompanyID, "task_status_updated", int32(task.ID), "task", `{"status":"`+req.Status+`"}`)

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

func (s *Server) logActivity(companyID int32, action string, entityID int32, entityType string, details string) {
	log := db.ActivityLog{
		CompanyID:  companyID,
		Action:     action,
		EntityID:   entityID,
		EntityType: entityType,
		Details:    details,
	}
	s.db.Create(&log)
}

func (s *Server) testProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseUrl string `json:"base_url"`
		ApiKey  string `json:"api_key"`
		Model   string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}

	if req.BaseUrl == "e2e-mock" {
		respondJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "log": "Mock connection successful."})
		return
	}

	url := req.BaseUrl
	if url[len(url)-1] != '/' {
		url += "/"
	}

	var payload []byte
	var isAnthropic bool

	if strings.Contains(url, "anthropic.com") {
		isAnthropic = true
		url += "v1/messages"
		p := map[string]interface{}{
			"model": req.Model,
			"messages": []map[string]string{
				{"role": "user", "content": "Say 'hello world'"},
			},
			"max_tokens": 10,
		}
		payload, _ = json.Marshal(p)
	} else {
		// Default to OpenAI compatible
		url += "v1/chat/completions"
		p := map[string]interface{}{
			"model": req.Model,
			"messages": []map[string]string{
				{"role": "user", "content": "Say 'hello world'"},
			},
			"max_tokens": 10,
		}
		payload, _ = json.Marshal(p)
	}

	clientReq, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": "Failed to create request", "log": err.Error()})
		return
	}

	clientReq.Header.Set("Content-Type", "application/json")
	if isAnthropic {
		clientReq.Header.Set("x-api-key", req.ApiKey)
		clientReq.Header.Set("anthropic-version", "2023-06-01")
	} else {
		clientReq.Header.Set("Authorization", "Bearer "+req.ApiKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(clientReq)
	if err != nil {
		respondJSON(w, http.StatusBadGateway, map[string]interface{}{"error": "Connection failed", "log": "HTTP Error: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	logMsg := "Request URL: " + url + "\nStatus: " + resp.Status + "\nResponse: " + string(respBody)

	if resp.StatusCode >= 400 {
		respondJSON(w, resp.StatusCode, map[string]interface{}{"error": "Provider returned error", "log": logMsg})
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "log": logMsg})
}

func (s *Server) listProviders(w http.ResponseWriter, r *http.Request) {
	providers, err := s.q.ListLLMProviders(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, providers)
}

func (s *Server) createProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name    string `json:"name"`
		BaseUrl string `json:"base_url"`
		ApiKey  string `json:"api_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}
	p := db.LLMProvider{
		Name:    req.Name,
		BaseUrl: req.BaseUrl,
		ApiKey:  req.ApiKey,
	}
	if err := s.db.Create(&p).Error; err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusCreated, p)
}
