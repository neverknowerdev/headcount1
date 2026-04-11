import re

with open("server/handlers.go", "r") as f:
    content = f.read()

# Replace the entire testProvider function
new_test_provider = """func (s *Server) testProvider(w http.ResponseWriter, r *http.Request) {
	var req struct {
		BaseUrl string `json:"base_url"`
		ApiKey  string `json:"api_key"`
		Model   string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "Invalid payload")
		return
	}
	if req.BaseUrl == "e2e-mock" {
		respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	payload := map[string]interface{}{
		"model": req.Model,
		"messages": []map[string]string{
			{"role": "user", "content": "Say 'hello world'"},
		},
		"max_tokens": 10,
	}

	bodyBytes, _ := json.Marshal(payload)
	url := req.BaseUrl
	if url[len(url)-1] != '/' {
		url += "/"
	}
	url += "v1/chat/completions"

	clientReq, err := http.NewRequest("POST", url, bytes.NewBuffer(bodyBytes))
	if err != nil {
		respondError(w, http.StatusInternalServerError, "Failed to create request")
		return
	}

	clientReq.Header.Set("Content-Type", "application/json")
	clientReq.Header.Set("Authorization", "Bearer "+req.ApiKey)

	client := &http.Client{}
	resp, err := client.Do(clientReq)
	if err != nil {
		respondError(w, http.StatusBadGateway, "Failed to contact provider: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respondError(w, resp.StatusCode, "Provider returned error status: " + resp.Status)
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}"""

content = re.sub(
    r'func \(s \*Server\) testProvider.*?respondJSON\(w, http\.StatusOK, map\[string\]string\{"status": "ok"\}\)\n\}',
    new_test_provider,
    content,
    flags=re.DOTALL
)

with open("server/handlers.go", "w") as f:
    f.write(content)
