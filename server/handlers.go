package server

import (
	"encoding/json"
	"net/http"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/server/controllers"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

type Server struct {
	db     *gorm.DB
	q      db.Querier
	hub    *eventhub.Hub
	engine *engine.OpenCodeEngine
}

func NewServer(database *gorm.DB, eng *engine.OpenCodeEngine) *Server {
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

	api := endpoints.NewAPI(s.db, s.engine, s.hub)

	r.Route("/companies", func(r chi.Router) {
		r.Get("/", api.ListCompanies)
		r.Post("/", api.CreateCompany)
		r.Put("/{id}", api.UpdateCompany)
	})

	r.Get("/settings", api.GetSettings)
	r.Post("/settings", api.UpdateSettings)
	r.Get("/activities", api.ListActivities)

	r.Route("/skills", func(r chi.Router) {
		r.Get("/", api.ListSkills)
		r.Post("/", api.CreateSkill)
		r.Get("/{id}/files", api.ListSkillFiles)
		r.Get("/{id}/files/content", api.GetSkillFileContent)
		r.Put("/{id}/files/content", api.UpdateSkillFileContent)
	})

	r.Route("/projects", func(r chi.Router) {
		r.Get("/", api.ListProjects)
		r.Post("/", api.CreateProject)
	})

	r.Route("/tasks", func(r chi.Router) {
		r.Get("/", api.ListTasks)
		r.Post("/", api.CreateTask)
		r.Get("/{id}", api.GetTask)
		r.Put("/{id}", api.UpdateTask)
		r.Put("/{id}/status", api.UpdateTask) // Keep for backward compatibility if needed, though they map to same
		r.Get("/{id}/runs", api.ListTaskRuns)
	})

	r.Route("/agents", func(r chi.Router) {
		r.Get("/", api.ListAgents)
		r.Post("/", api.CreateAgent)
		r.Get("/{id}", api.GetAgent)
		r.Put("/{id}", api.UpdateAgent)
		r.Get("/{id}/stats", api.GetAgentStats)
		r.Get("/{id}/runs", api.ListAgentRuns)
	})

	r.Route("/comments", func(r chi.Router) {
		r.Get("/", api.ListComments)
		r.Post("/", api.CreateComment)
	})

	r.Route("/attachments", func(r chi.Router) {
		r.Post("/", api.UploadAttachment)
	})

	r.Route("/sprints", func(r chi.Router) {
		r.Get("/", api.ListSprints)
		r.Post("/", api.CreateSprint)
	})

	r.Route("/runs", func(r chi.Router) {
		r.Get("/", api.ListCompanyRuns)
		r.Get("/{id}", api.GetRun)
	})

	r.Route("/providers", func(r chi.Router) {
		r.Get("/", api.ListProviders)
		r.Post("/", api.CreateProvider)
		r.Put("/{id}", api.UpdateProvider)
		r.Delete("/{id}", api.DeleteProvider)
		r.Post("/test", api.TestProvider)
	})
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

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

func (s *Server) serveWs(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	client := &eventhub.Client{Hub: s.hub, Conn: conn, Send: make(chan []byte, 256)}
	client.Hub.Register <- client

	go client.WritePump()
	go client.ReadPump()
}
