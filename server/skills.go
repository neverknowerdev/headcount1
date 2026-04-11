package server

import (
	"encoding/json"
		"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/filesystem"

	"github.com/go-chi/chi/v5"
	"gorm.io/gorm"
)

func registerSkillRoutes(r chi.Router, database *gorm.DB) {
	s := &Server{db: database, q: db.New(database)}

	r.Route("/skills", func(r chi.Router) {
		r.Get("/", s.listSkills)
		r.Post("/", s.createSkill)
		r.Get("/{id}/files", s.listSkillFiles)
		r.Get("/{id}/files/content", s.getSkillFileContent)
		r.Put("/{id}/files/content", s.updateSkillFileContent)
	})
}

func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
	compID, err := strconv.Atoi(r.URL.Query().Get("company_id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "company_id is required")
		return
	}
	var skills []db.Skill
	if err := s.db.Where("company_id = ?", compID).Find(&skills).Error; err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}
	respondJSON(w, http.StatusOK, skills)
}

func (s *Server) createSkill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CompanyID   int32  `json:"company_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	var comp db.Company
	if err := s.db.First(&comp, req.CompanyID).Error; err != nil {
		respondError(w, http.StatusNotFound, "Company not found")
		return
	}

	settings := LoadSettings()
	fsManager := filesystem.NewManager(settings.BasePath)

	skill := db.Skill{
		CompanyID:   req.CompanyID,
		Name:        req.Name,
		Description: req.Description,
		LocalPath:   fsManager.GetSkillPath(comp, db.Skill{Name: req.Name}),
	}

	if err := s.db.Create(&skill).Error; err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := fsManager.CreateSkillDirectory(comp, skill); err != nil {
		println("Error creating skill directory:", err.Error())
	}

	s.logActivity(comp.ID, "skill_created", int32(skill.ID), "skill", "")

	respondJSON(w, http.StatusCreated, skill)
}

func (s *Server) listSkillFiles(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var skill db.Skill
	if err := s.db.First(&skill, id).Error; err != nil {
		respondError(w, http.StatusNotFound, "Skill not found")
		return
	}

	files := make([]string, 0)
	if _, err := os.Stat(skill.LocalPath); !os.IsNotExist(err) {
		err = filepath.Walk(skill.LocalPath, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() {
				rel, _ := filepath.Rel(skill.LocalPath, path)
				files = append(files, rel)
			}
			return nil
		})
		if err != nil {
			respondError(w, http.StatusInternalServerError, "Failed to list files: "+err.Error())
			return
		}
	}

	respondJSON(w, http.StatusOK, files)
}

func (s *Server) getSkillFileContent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}

	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		respondError(w, http.StatusBadRequest, "path is required")
		return
	}

	var skill db.Skill
	if err := s.db.First(&skill, id).Error; err != nil {
		respondError(w, http.StatusNotFound, "Skill not found")
		return
	}

	// Security check to prevent path traversal
	fullPath := filepath.Join(skill.LocalPath, filePath)
	if !strings.HasPrefix(filepath.Clean(fullPath), filepath.Clean(skill.LocalPath)) {
		respondError(w, http.StatusBadRequest, "Invalid path")
		return
	}

	content, err := os.ReadFile(fullPath)
	if err != nil {
		respondError(w, http.StatusNotFound, "File not found or unreadable")
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Write(content)
}

func (s *Server) updateSkillFileContent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		respondError(w, http.StatusBadRequest, "invalid id")
		return
	}

	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	var skill db.Skill
	if err := s.db.First(&skill, id).Error; err != nil {
		respondError(w, http.StatusNotFound, "Skill not found")
		return
	}

	// Security check to prevent path traversal
	fullPath := filepath.Join(skill.LocalPath, req.Path)
	if !strings.HasPrefix(filepath.Clean(fullPath), filepath.Clean(skill.LocalPath)) {
		respondError(w, http.StatusBadRequest, "Invalid path")
		return
	}

	// Create directory if not exists
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to create directory: "+err.Error())
		return
	}

	if err := os.WriteFile(fullPath, []byte(req.Content), 0644); err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to write file: "+err.Error())
		return
	}

	s.logActivity(skill.CompanyID, "skill_file_updated", int32(skill.ID), "skill", `{"file":"`+req.Path+`"}`)

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
