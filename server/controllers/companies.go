package endpoints

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"
	"github.com/go-chi/chi/v5"
)

func (api *API) ListCompanies(w http.ResponseWriter, r *http.Request) {
	companies, err := api.q.ListCompanies(r.Context())
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	settings := LoadSettings()
	fsManager := filesystem.NewManager(settings.BasePath)
	validCompanies := []db.Company{}
	for _, c := range companies {
		if fsManager.CompanyExists(c) {
			validCompanies = append(validCompanies, c)
		}
	}

	api.respondJSON(w, http.StatusOK, validCompanies)
}

func (api *API) CreateCompany(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
		Color     string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	comp := db.Company{
		Name:      req.Name,
		ShortName: req.ShortName,
		Color:     req.Color,
	}

	if err := api.db.Create(&comp).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	settings := LoadSettings()
	fsManager := filesystem.NewManager(settings.BasePath)
	if err := fsManager.CreateCompanyDirectories(comp); err != nil {
		// Log error but don't fail the request completely
		println("Error creating company directories:", err.Error())
	}

	// Write company metadata to filesystem
	storage := filesystem.NewStorage(settings.BasePath)
	if err := storage.WriteCompany(comp); err != nil {
		log.Printf("Warning: failed to write company metadata: %v", err)
	}

	api.logActivity(comp.ID, "company_created", int32(comp.ID), "company", "")

	api.respondJSON(w, http.StatusCreated, comp)
}

func (api *API) UpdateCompany(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid ID")
		return
	}

	var req struct {
		ShortName string `json:"short_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	var comp db.Company
	if err := api.db.First(&comp, id).Error; err != nil {
		api.respondError(w, http.StatusNotFound, "Company not found")
		return
	}

	oldShortName := comp.ShortName
	comp.ShortName = req.ShortName
	if err := api.db.Save(&comp).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Rename company directory on disk if shortname changed
	if oldShortName != req.ShortName {
		settings := LoadSettings()
		fsManager := filesystem.NewManager(settings.BasePath)
		oldPath := filepath.Join(fsManager.GetBasePath(), "data", oldShortName)
		newPath := filepath.Join(fsManager.GetBasePath(), "data", req.ShortName)
		if _, err := os.Stat(oldPath); err == nil {
			if err := os.Rename(oldPath, newPath); err != nil {
				log.Printf("Warning: failed to rename company directory from %s to %s: %v", oldPath, newPath, err)
			}
		}
	}

	api.respondJSON(w, http.StatusOK, comp)
}

func (api *API) DeleteCompany(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid ID")
		return
	}

	var comp db.Company
	if err := api.db.First(&comp, id).Error; err != nil {
		api.respondError(w, http.StatusNotFound, "Company not found")
		return
	}

	settings := LoadSettings()
	fsManager := filesystem.NewManager(settings.BasePath)

	archivePath, err := fsManager.ArchiveCompany(comp)
	if err != nil {
		log.Printf("Warning: failed to archive company files: %v", err)
	}

	if err := fsManager.DeleteCompanyFiles(comp); err != nil {
		log.Printf("Warning: failed to delete company files: %v", err)
	}

	// Delete related records first (in order to respect FK dependencies)
	api.db.Where("company_id = ?", comp.ID).Delete(&db.ActivityLog{})
	api.db.Where("company_id = ?", comp.ID).Delete(&db.Skill{})
	api.db.Where("company_id = ?", comp.ID).Delete(&db.Task{})
	api.db.Where("company_id = ?", comp.ID).Delete(&db.Project{})
	api.db.Where("company_id = ?", comp.ID).Delete(&db.Agent{})
	api.db.Where("company_id = ?", comp.ID).Delete(&db.Sprint{})

	result := api.db.Delete(&comp)
	if result.Error != nil {
		api.respondError(w, http.StatusInternalServerError, result.Error.Error())
		return
	}

	api.respondJSON(w, http.StatusOK, map[string]interface{}{
		"message":      "Company deleted successfully",
		"archive_path": archivePath,
	})
}
