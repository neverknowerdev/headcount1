import re

with open("server/skills.go", "r") as f:
    content = f.read()

content = content.replace('"io"\n', '')

# Replace s.q.ListSkillsByCompany with direct db call
new_list_skills = """func (s *Server) listSkills(w http.ResponseWriter, r *http.Request) {
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
}"""

content = re.sub(
    r'func \(s \*Server\) listSkills.*?respondJSON\(w, http\.StatusOK, skills\)\n\}',
    new_list_skills,
    content,
    flags=re.DOTALL
)

with open("server/skills.go", "w") as f:
    f.write(content)
