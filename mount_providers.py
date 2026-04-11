import re

with open("server/handlers.go", "r") as f:
    content = f.read()

# Make sure Mount function includes providers
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
