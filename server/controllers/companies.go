package endpoints

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/classifier"
	"agent-orchestrator/pkg/agentdefaults"
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
		Name        string `json:"name"`
		ShortName   string `json:"short_name"`
		Description string `json:"description"`
		Color       string `json:"color"`
		ProviderID  *int32 `json:"provider_id"`
		Model       string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	uid := api.currentUserID(r)
	comp := db.Company{
		Name:        req.Name,
		ShortName:   req.ShortName,
		Description: req.Description,
		Color:       req.Color,
		UserID:      &uid, // creator (engine resolves their Default Models)
	}
	if membership, err := api.requireMembership(r); err == nil {
		comp.TeamID = &membership.TeamID
	}
	if req.ProviderID != nil {
		if err := api.authorizeModelBinding(r, req.ProviderID, nil); err != nil {
			api.respondError(w, http.StatusNotFound, "provider not found")
			return
		}
	} else {
		// API clients often create the provider immediately before the company
		// and omit the optional binding. Use the user's first enabled provider so
		// the first task has a model to run on.
		if providers, err := api.q.ListLLMProvidersForUser(r.Context(), api.currentUserID(r)); err == nil {
			for _, provider := range providers {
				// A classifier is not a model a task can run on.
				if provider.Enabled && provider.ProviderType != classifier.ProviderType {
					providerID := provider.ID
					req.ProviderID = &providerID
					if req.Model == "" {
						req.Model = provider.DefaultModel
					}
					break
				}
			}
		}
	}

	if err := api.db.Create(&comp).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := api.q.EnsureBuiltinAgentsForCompany(r.Context(), comp.ID, agentdefaults.Rows(comp.ID)); err != nil {
		api.respondError(w, http.StatusInternalServerError, "failed to seed built-in agents: "+err.Error())
		return
	}
	if req.ProviderID != nil && req.Model != "" && !api.isClassifierProvider(r.Context(), *req.ProviderID) {
		if err := api.fillModelTiers(r.Context(), uid, *req.ProviderID, req.Model); err != nil {
			api.respondError(w, http.StatusInternalServerError, "failed to set default models: "+err.Error())
			return
		}
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

func (api *API) isClassifierProvider(ctx context.Context, providerID int32) bool {
	provider, err := api.q.GetLLMProvider(ctx, providerID)
	return err == nil && provider.ProviderType == classifier.ProviderType
}

// fillModelTiers points the user's smart and cheap model slots at a provider
// model where they are still empty, so a first company can run tasks without
// a visit to Default Models. A slot the user already configured is left alone.
func (api *API) fillModelTiers(ctx context.Context, userID, providerID int32, model string) error {
	if err := api.q.EnsureDefaultModelSettingsForUser(ctx, userID); err != nil {
		return err
	}
	for _, purpose := range []string{db.PurposeSmart, db.PurposeCheap} {
		setting, err := api.q.GetDefaultModelSetting(ctx, userID, purpose)
		if err != nil {
			return err
		}
		if setting.ProviderID != nil || setting.ModelGroupID != nil {
			continue
		}
		if _, err := api.q.UpdateDefaultModelSetting(ctx, userID, purpose, &providerID, model, nil); err != nil {
			return err
		}
	}
	return nil
}

func (api *API) UpdateCompany(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name        *string `json:"name"`
		ShortName   *string `json:"short_name"`
		Description *string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	comp := api.companyFromCtx(r) // loaded + authorized by LoadCompany

	oldShortName := comp.ShortName
	if req.Name != nil {
		comp.Name = *req.Name
	}
	if req.ShortName != nil {
		comp.ShortName = *req.ShortName
	}
	if req.Description != nil {
		comp.Description = *req.Description
	}
	if err := api.db.Save(&comp).Error; err != nil {
		api.respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Rename the company-scoped directories on disk if the shortname changed.
	if req.ShortName != nil && oldShortName != *req.ShortName {
		settings := LoadSettings()
		paths := filesystem.NewPaths(settings.BasePath)
		oldDirs := paths.CompanyDirs(oldShortName)
		newDirs := paths.CompanyDirs(*req.ShortName)
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
