package endpoints

import (
	"encoding/json"
	"net/http"
	"strconv"

	"agent-orchestrator/db"
)

func (api *API) ListActivities(w http.ResponseWriter, r *http.Request) {
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
	if _, err := api.authorizeCompany(r, int32(companyID)); err != nil {
		http.Error(w, "company not found", http.StatusNotFound)
		return
	}

	var logs []db.ActivityLog
	if err := api.db.Where("company_id = ?", companyID).Order("created_at desc").Limit(50).Find(&logs).Error; err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logs)
}
