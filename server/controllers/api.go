package endpoints

import (
	"encoding/json"
	"net/http"

	"agent-orchestrator/db"
	"agent-orchestrator/engine"
	"agent-orchestrator/eventhub"
	"gorm.io/gorm"
)

type API struct {
	db     *gorm.DB
	q      *db.Queries
	engine *engine.OpenCodeEngine
	hub    *eventhub.Hub
}

func NewAPI(database *gorm.DB, eng *engine.OpenCodeEngine, h *eventhub.Hub) *API {
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
	log := db.ActivityLog{
		CompanyID:  companyID,
		Action:     action,
		EntityID:   entityID,
		EntityType: entityType,
		Details:    details,
	}
	api.db.Create(&log)
}
