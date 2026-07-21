package endpoints

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"
)

func (api *API) ListCompanies(w http.ResponseWriter, r *http.Request) {
	companies, err := api.q.ListCompaniesForUser(r.Context(), api.currentUserID(r))
	if err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	api.respondJSON(w, http.StatusOK, companies)
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

	uid := api.currentUserID(r)
	comp := db.Company{
		Name:      req.Name,
		ShortName: req.ShortName,
		Color:     req.Color,
		UserID:    &uid, // creator (engine resolves their Default Models)
	}
	if membership, err := api.requireMembership(r); err == nil {
		comp.TeamID = &membership.TeamID
	}

	if err := api.db.Create(&comp).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	settings := LoadSettings()
	fsManager := filesystem.NewManager(settings.BasePath)
	if err := fsManager.CreateCompanyDirectories(comp); err != nil {
		// Log error but don't fail the request completely
		log.Printf("Error creating company directories: %v", err)
	}

	api.logActivity(comp.ID, "company_created", int32(comp.ID), "company", "")

	api.respondJSON(w, http.StatusCreated, comp)
}

func (api *API) UpdateCompany(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ShortName string `json:"short_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	comp := api.companyFromCtx(r) // loaded + authorized by LoadCompany

	oldShortName := comp.ShortName
	comp.ShortName = req.ShortName
	if err := api.db.Save(&comp).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Rename the company-scoped directories on disk if the shortname changed.
	if oldShortName != req.ShortName {
		settings := LoadSettings()
		paths := filesystem.NewPaths(settings.BasePath)
		oldDirs := paths.CompanyDirs(oldShortName)
		newDirs := paths.CompanyDirs(req.ShortName)
		for i := range oldDirs {
			if _, err := os.Stat(oldDirs[i]); err != nil {
				continue
			}
			if err := os.Rename(oldDirs[i], newDirs[i]); err != nil {
				log.Printf("Warning: failed to rename company directory from %s to %s: %v", oldDirs[i], newDirs[i], err)
			}
		}
	}

	api.respondJSON(w, http.StatusOK, comp)
}

func (api *API) DeleteCompany(w http.ResponseWriter, r *http.Request) {
	comp := api.companyFromCtx(r) // loaded + authorized by LoadCompany

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
