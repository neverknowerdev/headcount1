import re

with open("server/handlers.go", "r") as f:
    content = f.read()

# Make sure we mount the proxy properly.
# Also, let's fix testProvider mock logic completely because it seems the route is failing or not hit (404)
new_test_provider = """func (s *Server) testProvider(w http.ResponseWriter, r *http.Request) {
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
		respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	payload := map[string]interface{}{
		"model": req.Model,
		"messages": []map[string]string{
			{"role": "user", "content": "Say 'hello world'"},
		},
		"max_tokens": 10,
	}

	bodyBytes, _ := json.Marshal(payload)
	url := req.BaseUrl
	if url[len(url)-1] != '/' {
		url += "/"
	}
	url += "v1/chat/completions"

	clientReq, err := http.NewRequest("POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to create request")
		return
	}

	clientReq.Header.Set("Content-Type", "application/json")
	clientReq.Header.Set("Authorization", "Bearer "+req.ApiKey)

	client := &http.Client{}
	resp, err := client.Do(clientReq)
	if err != nil {
		respondError(w, http.StatusBadGateway, "Failed to contact provider: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respondError(w, resp.StatusCode, "Provider returned error status: " + resp.Status)
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}"""

content = re.sub(
    r'func \(s \*Server\) testProvider.*?respondJSON\(w, http\.StatusOK, map\[string\]string\{"status": "ok"\}\)\n\}',
    new_test_provider,
    content,
    flags=re.DOTALL
)

# Fix Mount function to actually mount the providers endpoint properly
new_mount = """func (s *Server) Mount(r chi.Router) {
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

	r.Route("/agents", func(r chi.Router) {
		r.Get("/", s.listAgents)
		r.Post("/", s.createAgent)
	})

	r.Route("/tasks", func(r chi.Router) {
		r.Get("/", s.listTasks)
		r.Post("/", s.createTask)
		r.Get("/{id}", s.getTask)
		r.Put("/{id}/status", s.updateTaskStatus)
	})

	r.Route("/comments", func(r chi.Router) {
		r.Get("/", s.listComments)
		r.Post("/", s.createComment)
	})

	r.Route("/attachments", func(r chi.Router) {
		r.Post("/", s.uploadAttachment)
	})

	r.Route("/providers", func(r chi.Router) {
		r.Get("/", s.listProviders)
		r.Post("/", s.createProvider)
		r.Post("/test", s.testProvider)
	})
}"""

content = re.sub(
    r'func \(s \*Server\) Mount\(r chi\.Router\) \{.*?\nr\.Route\("/attachments", func\(r chi\.Router\) \{\n\t\tr\.Post\("/", s\.uploadAttachment\)\n\t\}\)\n\}',
    new_mount,
    content,
    flags=re.DOTALL
)

with open("server/handlers.go", "w") as f:
    f.write(content)
