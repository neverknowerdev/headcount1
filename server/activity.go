package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"agent-orchestrator/db"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

func registerActivityRoutes(r chi.Router, database *gorm.DB) {
	r.Get("/activities", func(w http.ResponseWriter, r *http.Request) {
		companyIDStr := r.URL.Query().Get("company_id")
		if companyIDStr == "" {
			http.Error(w, "company_id is required", http.StatusBadRequest)
			return
		}

		companyID, err := strconv.Atoi(companyIDStr)
		if err != nil {
			http.Error(w, "invalid company_id", http.StatusBadRequest)
			return
		}

		var logs []db.ActivityLog
		if err := database.Where("company_id = ?", companyID).Order("created_at desc").Limit(50).Find(&logs).Error; err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(logs)
	})
}
