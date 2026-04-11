import re

with open("server/handlers.go", "r") as f:
    content = f.read()

# Make sure pkg/filesystem is imported
if '"agent-orchestrator/pkg/filesystem"' not in content:
    content = content.replace('"agent-orchestrator/engine"', '"agent-orchestrator/engine"\n\t"agent-orchestrator/pkg/filesystem"')

# Update createCompany to also create fs directories
new_create_company = """func (s *Server) createCompany(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
		Color     string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid request payload")
		return
	}

	comp := db.Company{
		Name:      req.Name,
		ShortName: req.ShortName,
		Color:     req.Color,
	}

	if err := s.db.Create(&comp).Error; err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	settings := LoadSettings()
	fsManager := filesystem.NewManager(settings.BasePath)
	if err := fsManager.CreateCompanyDirectories(comp); err != nil {
		// Log error but don't fail the request completely
		println("Error creating company directories:", err.Error())
	}

	s.logActivity(comp.ID, "company_created", int32(comp.ID), "company", "")

	respondJSON(w, http.StatusCreated, comp)
}"""

content = re.sub(
    r'func \(s \*Server\) createCompany.*?respondJSON\(w, http\.StatusCreated, comp\)\n\}',
    new_create_company,
    content,
    flags=re.DOTALL
)

with open("server/handlers.go", "w") as f:
    f.write(content)
