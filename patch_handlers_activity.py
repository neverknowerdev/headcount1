import re

with open("server/handlers.go", "r") as f:
    content = f.read()

# Update createProject to log activity and create fs dir
new_create_project = """func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
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
}"""

content = re.sub(
    r'func \(s \*Server\) createProject.*?respondJSON\(w, http\.StatusCreated, proj\)\n\}',
    new_create_project,
    content,
    flags=re.DOTALL
)

# Update createTask to log activity and create fs dir
new_create_task = """func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
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

	respondJSON(w, http.StatusCreated, task)
}"""

content = re.sub(
    r'func \(s \*Server\) createTask.*?respondJSON\(w, http\.StatusCreated, task\)\n\}',
    new_create_task,
    content,
    flags=re.DOTALL
)

# Update updateTaskStatus to log activity
new_update_task = """func (s *Server) updateTaskStatus(w http.ResponseWriter, r *http.Request) {
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
}"""

content = re.sub(
    r'func \(s \*Server\) updateTaskStatus.*?respondJSON\(w, http\.StatusOK, task\)\n\}',
    new_update_task,
    content,
    flags=re.DOTALL
)

# Update createAgent to log activity
new_create_agent = """func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
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
}"""

content = re.sub(
    r'func \(s \*Server\) createAgent.*?respondJSON\(w, http\.StatusCreated, agent\)\n\}',
    new_create_agent,
    content,
    flags=re.DOTALL
)

with open("server/handlers.go", "w") as f:
    f.write(content)
