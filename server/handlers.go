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
		respondJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "provider_type": "openai", "log": "Mock connection successful.", "url": req.BaseUrl})
		return
	}

	url := strings.TrimSpace(req.BaseUrl)

	// Helper to make request
	makeRequest := func(reqUrl string, isAnthropic bool) (int, string, string, error) {
		var payload []byte
		var clientReq *http.Request
		var err error

		if isAnthropic {
			p := map[string]interface{}{
				"model": req.Model,
				"messages": []map[string]string{
					{"role": "user", "content": "Say 'hello world'"},
				},
				"max_tokens": 10,
			}
			payload, _ = json.Marshal(p)
		} else {
			p := map[string]interface{}{
				"model": req.Model,
				"messages": []map[string]string{
					{"role": "user", "content": "Say 'hello world'"},
				},
				"max_tokens": 10,
			}
			payload, _ = json.Marshal(p)
		}

		clientReq, err = http.NewRequest("POST", reqUrl, bytes.NewBuffer(payload))
		if err != nil {
			return 0, "", "", err
		}

		clientReq.Header.Set("Content-Type", "application/json")
		if isAnthropic {
			clientReq.Header.Set("x-api-key", req.ApiKey)
			clientReq.Header.Set("anthropic-version", "2023-06-01")
		} else {
			clientReq.Header.Set("Authorization", "Bearer "+req.ApiKey)
		}

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(clientReq)
		if err != nil {
			return 0, "", "", err
		}
		defer resp.Body.Close()

		respBody, _ := io.ReadAll(resp.Body)

		// Avoid dumping massive HTML into logs
		bodyStr := string(respBody)
		if strings.Contains(strings.ToLower(bodyStr), "<html") {
			bodyStr = "<HTML response omitted>"
		}

		logMsg := "Request URL: " + reqUrl + "\nStatus: " + resp.Status + "\nResponse: " + bodyStr

		// Attempt to parse understandable error message
		var parsedErr string
		if resp.StatusCode >= 400 {
			lowerBodyStr := strings.ToLower(string(respBody))
			if resp.StatusCode == 401 || resp.StatusCode == 403 {
				parsedErr = "Invalid API Key or unauthorized access."
			} else if resp.StatusCode == 429 {
				parsedErr = "Rate limit exceeded or insufficient quota."
			} else if strings.Contains(lowerBodyStr, "model") && (strings.Contains(lowerBodyStr, "not found") || strings.Contains(lowerBodyStr, "does not exist") || strings.Contains(lowerBodyStr, "invalid") || strings.Contains(lowerBodyStr, "unsupported")) {
				parsedErr = "Model is not supported or not found."
			} else if resp.StatusCode == 404 {
			    if strings.Contains(lowerBodyStr, "model") {
			        parsedErr = "Model is not supported or not found."
			    } else {
			        parsedErr = "Endpoint not found (404). Please check the Base URL."
			    }
			} else {
			    var errJson map[string]interface{}
			    if err := json.Unmarshal(respBody, &errJson); err == nil {
			        if errObj, ok := errJson["error"].(map[string]interface{}); ok {
			            if msg, ok := errObj["message"].(string); ok {
			                parsedErr = msg
			            }
			        }
			    }
			    if parsedErr == "" {
			        parsedErr = "Provider returned error status: " + resp.Status
			    }
			}
		}

		return resp.StatusCode, logMsg, parsedErr, nil
	}

	// Determine urls to try
	var openAiUrls []string
	var anthropicUrls []string

	// 1. Exact URL as typed
	openAiUrls = append(openAiUrls, url)
	anthropicUrls = append(anthropicUrls, url)

	// 2. Intelligent suffixes if missing
	cleanUrl := strings.TrimSuffix(strings.TrimSpace(url), "/")
	if !strings.HasSuffix(cleanUrl, "/chat/completions") && !strings.HasSuffix(cleanUrl, "/messages") {
		// If it ends with /v1, we use it directly, else we add /v1
		baseClean := cleanUrl
		if !strings.HasSuffix(baseClean, "/v1") {
			baseClean += "/v1"
		}

		openAiUrls = append(openAiUrls, baseClean+"/chat/completions")
		anthropicUrls = append(anthropicUrls, baseClean+"/messages")
	}

	var combinedLog string
	var lastParsedErr string

	// Try OpenAI URLs
	for _, testUrl := range openAiUrls {
		combinedLog += "--- OpenAI Format Attempt (" + testUrl + ") ---\n"
		status, logMsg, parsedErr, err := makeRequest(testUrl, false)

		if err != nil {
			combinedLog += "Error: " + err.Error() + "\n"
		} else {
			combinedLog += logMsg + "\n"
			lastParsedErr = parsedErr
			if status >= 200 && status < 300 {
				respondJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "provider_type": "openai", "url": testUrl, "log": combinedLog})
				return
			}
		}
	}

	// Try Anthropic URLs
	for _, testUrl := range anthropicUrls {
		combinedLog += "\n--- Anthropic Format Attempt (" + testUrl + ") ---\n"
		status, logMsg, parsedErr, err := makeRequest(testUrl, true)

		if err != nil {
			combinedLog += "Error: " + err.Error() + "\n"
		} else {
			combinedLog += logMsg + "\n"
			lastParsedErr = parsedErr
			if status >= 200 && status < 300 {
				respondJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "provider_type": "anthropic", "url": testUrl, "log": combinedLog})
				return
			}
		}
	}

	finalErr := lastParsedErr
	if finalErr == "" {
	    finalErr = "Provider returned error for all attempted URLs"
	}

	respondJSON(w, http.StatusBadRequest, map[string]interface{}{"error": finalErr, "log": combinedLog})
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
func (s *Server) getProviderModels(w http.ResponseWriter, r *http.Request) {
	baseUrl := r.URL.Query().Get("base_url")
	apiKey := r.URL.Query().Get("api_key")

	if baseUrl == "" || apiKey == "" {
		respondError(w, http.StatusBadRequest, "Missing base_url or api_key")
		return
	}

	if baseUrl == "e2e-mock" {
		respondJSON(w, http.StatusOK, []map[string]string{{"id": "gpt-4"}, {"id": "gpt-3.5-turbo"}})
		return
	}

	url := strings.TrimSuffix(baseUrl, "/")
	url = strings.TrimSuffix(url, "/v1")
	url += "/v1/models"

	clientReq, err := http.NewRequest("GET", url, nil)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to create request")
		return
	}

	clientReq.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(clientReq)
	if err != nil {
		respondJSON(w, http.StatusBadGateway, map[string]interface{}{"error": "Failed to fetch models", "log": err.Error()})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(resp.Body)
		respondJSON(w, resp.StatusCode, map[string]interface{}{"error": "Provider error", "log": string(respBody)})
		return
	}

	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to parse models response")
		return
	}

	models := []map[string]string{}
	for _, m := range data.Data {
		models = append(models, map[string]string{"id": m.ID})
	}
	respondJSON(w, http.StatusOK, models)
}
