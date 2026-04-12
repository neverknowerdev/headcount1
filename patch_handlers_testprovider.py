import re

with open("server/handlers.go", "r") as f:
    content = f.read()

# Update testProvider to handle anthropic and openai interchangeably, and return full error logs
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
		respondJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "log": "Mock connection successful."})
		return
	}

	url := req.BaseUrl
	if url[len(url)-1] != '/' {
		url += "/"
	}

	var payload []byte
	var isAnthropic bool

	if strings.Contains(url, "anthropic.com") {
		isAnthropic = true
		url += "v1/messages"
		p := map[string]interface{}{
			"model": req.Model,
			"messages": []map[string]string{
				{"role": "user", "content": "Say 'hello world'"},
			},
			"max_tokens": 10,
		}
		payload, _ = json.Marshal(p)
	} else {
		// Default to OpenAI compatible
		url += "v1/chat/completions"
		p := map[string]interface{}{
			"model": req.Model,
			"messages": []map[string]string{
				{"role": "user", "content": "Say 'hello world'"},
			},
			"max_tokens": 10,
		}
		payload, _ = json.Marshal(p)
	}

	clientReq, err := http.NewRequest("POST", url, bytes.NewBuffer(payload))
	if err != nil {
		respondJSON(w, http.StatusInternalServerError, map[string]interface{}{"error": "Failed to create request", "log": err.Error()})
		return
	}

	clientReq.Header.Set("Content-Type", "application/json")
	if isAnthropic {
		clientReq.Header.Set("x-api-key", req.ApiKey)
		clientReq.Header.Set("anthropic-version", "2023-06-01")
	} else {
		clientReq.Header.Set("Authorization", "Bearer "+req.ApiKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(clientReq)
	if err != nil {
		respondJSON(w, http.StatusBadGateway, map[string]interface{}{"error": "Connection failed", "log": "HTTP Error: " + err.Error()})
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	logMsg := "Request URL: " + url + "\\nStatus: " + resp.Status + "\\nResponse: " + string(respBody)

	if resp.StatusCode >= 400 {
		respondJSON(w, resp.StatusCode, map[string]interface{}{"error": "Provider returned error", "log": logMsg})
		return
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "log": logMsg})
}"""

content = re.sub(
    r'func \(s \*Server\) testProvider\(w http\.ResponseWriter, r \*http\.Request\) \{.*?respondJSON\(w, http\.StatusOK, map\[string\]string\{"status": "ok"\}\)\n\}',
    new_test_provider,
    content,
    flags=re.DOTALL
)

if '"strings"' not in content:
    content = content.replace('"time"\n', '"time"\n\t"strings"\n')

with open("server/handlers.go", "w") as f:
    f.write(content)
