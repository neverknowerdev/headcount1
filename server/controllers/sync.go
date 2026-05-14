package endpoints

import (
	"context"
	"net/http"
	"log"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"
)

func (api *API) SyncDBWithFilesystem(ctx context.Context) error {
	settings := LoadSettings()
	log.Printf("Syncing DB with filesystem at %s", settings.BasePath)
	fm := filesystem.NewManager(settings.BasePath)
	if err := fm.SetupBaseDirectories(); err != nil {
		return err
	}

	// 1. Sync Companies
	compShortNames, err := fm.ListCompanies()
	if err != nil {
		log.Printf("Error listing companies: %v", err)
		return err
	}
	log.Printf("Found %d companies on disk: %v", len(compShortNames), compShortNames)

	for _, shortName := range compShortNames {
		var existing db.Company
		if err := api.db.Where("short_name = ?", shortName).First(&existing).Error; err != nil {
			log.Printf("Creating missing company in DB: %s", shortName)
			// Not found, create it
			newComp := db.Company{
				Name:      shortName,
				ShortName: shortName,
				Color:     "#4F46E5",
			}
			if err := api.db.Create(&newComp).Error; err == nil {
				existing = newComp
			} else {
				log.Printf("Failed to create company %s: %v", shortName, err)
				continue
			}
		}

		// 2. Sync Projects for this company
		projNames, err := fm.ListProjects(shortName)
		if err != nil {
			continue
		}
		log.Printf("Found %d projects for %s: %v", len(projNames), shortName, projNames)

		for _, projName := range projNames {
			var existingProj db.Project
			if err := api.db.Where("company_id = ? AND name = ?", existing.ID, projName).First(&existingProj).Error; err != nil {
				log.Printf("Creating missing project in DB: %s/%s", shortName, projName)
				// Not found, create it
				newProj := db.Project{
					CompanyID:       int32(existing.ID),
					Name:            projName,
					WorkspaceFolder: shortName + "/" + projName,
				}
				api.db.Create(&newProj)
			}
		}
	}

	return nil
}

func (api *API) SyncSettings(w http.ResponseWriter, r *http.Request) {
	err := api.SyncDBWithFilesystem(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
