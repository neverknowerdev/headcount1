import re

with open("server/handlers.go", "r") as f:
    content = f.read()

# Make sure log the request to see why it fails
test_provider = """func (s *Server) testProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseUrl string `json:"base_url"`
		ApiKey  string `json:"api_key"`
		Model   string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}
	fmt.Println("TEST PROVIDER:", req.BaseUrl)
	if req.BaseUrl == "e2e-mock" {
		respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}"""

content = re.sub(
    r'func \(s \*Server\) testProvider\(w http\.ResponseWriter, r \*http\.Request\) \{\n\tvar req struct \{\n\t\tBaseUrl string `json:"base_url"`\n\t\tApiKey  string `json:"api_key"`\n\t\tModel   string `json:"model"`\n\t\}\n\tif err := json\.NewDecoder\(r\.Body\)\.Decode\(&req\); err != nil \{\n\t\trespondError\(w, http\.StatusBadRequest, "Invalid payload"\)\n\t\treturn\n\t\}\n\tif req\.BaseUrl == "e2e-mock" \{\n\t\trespondJSON\(w, http\.StatusOK, map\[string\]string\{"status": "ok"\}\)\n\t\treturn\n\t\}',
    test_provider,
    content,
    flags=re.DOTALL
)

if '"fmt"' not in content:
    content = content.replace('"bytes"\n', '"bytes"\n\t"fmt"\n')

with open("server/handlers.go", "w") as f:
    f.write(content)
