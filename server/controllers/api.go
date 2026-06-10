package endpoints

import (
	"encoding/json"
	"log"
	"net/http"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/eventhub"
	"agent-orchestrator/pkg/filesystem"
	"gorm.io/gorm"
)

type API struct {
	db     *gorm.DB
	q      *db.Queries
	engine engine.Engine
	hub    *eventhub.Hub
}

func NewAPI(database *gorm.DB, eng engine.Engine, h *eventhub.Hub) *API {
	return &API{
		db:     database,
		q:      db.New(database),
		engine: eng,
		hub:    h,
	}
}

func (api *API) respondJSON(w http.ResponseWriter, status int, data interface{}) {
	response, err := json.Marshal(data)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(response)
}

func (api *API) respondError(w http.ResponseWriter, status int, message string) {
	api.respondJSON(w, status, map[string]string{"error": message})
}

func (api *API) logActivity(companyID int32, action string, entityID int32, entityType string, details string) {
	activityLog := db.ActivityLog{
		CompanyID:  companyID,
		Action:     action,
		EntityID:   entityID,
		EntityType: entityType,
		Details:    details,
	}
	api.db.Create(&activityLog)

	// Write activity log to filesystem
	settings := LoadSettings()
	storage := filesystem.NewStorage(settings.BasePath)
	if err := storage.WriteActivityLog(activityLog); err != nil {
		log.Printf("Warning: failed to write activity log to filesystem: %v", err)
	}
}
