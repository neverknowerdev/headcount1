package server

import (
	endpoints "agent-orchestrator/server/controllers"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func skipTestProviderConnection(t *testing.T) {
	alibabaKey := os.Getenv("ALIBABA_CLOUD_API_KEY")

	if alibabaKey == "" || strings.HasPrefix(alibabaKey, "dummy") {
		t.Skip("Skipping provider test since real API keys are not provided in env")
	}

	api := &endpoints.API{} // Mock API, we just want to test the handler logic

	runTest := func(name, url, key, model string) {
		t.Run(name, func(t *testing.T) {
			payload := map[string]string{
				"base_url": url,
				"api_key":  key,
				"model":    model,
			}
			b, _ := json.Marshal(payload)
			req, err := http.NewRequest("POST", "/test", bytes.NewBuffer(b))
			assert.NoError(t, err)

			rr := httptest.NewRecorder()
			api.TestProvider(rr, req)

			assert.Equal(t, http.StatusOK, rr.Code, "Expected OK status for %s, got: %v", name, rr.Body.String())

			var res map[string]interface{}
			json.Unmarshal(rr.Body.Bytes(), &res)
			assert.Equal(t, "ok", res["status"])
		})
	}

	runTest("Alibaba DashScope", "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", alibabaKey, "qwen-plus")
}

// A provider that refuses a call says why, and that is what the user needs to
// read: a free model that only answers the provider's own client is not a bad
// key. Only a refusal with no explanation is put down to the key.
func TestProviderConnectionTestReportsWhyAProviderRefused(t *testing.T) {
	refusal := `{"type":"error","error":{"type":"FreeTierError","message":"OpenCode's free tier can only be used from within OpenCode"}}`
	run := func(forbiddenBody string) map[string]interface{} {
		provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.HasSuffix(r.URL.Path, "/chat/completions") {
				w.WriteHeader(http.StatusForbidden)
				w.Write([]byte(forbiddenBody))
				return
			}
			// The other request shape carries the key where this provider
			// does not look for it.
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"type":"error","error":{"type":"AuthError","message":"Missing API key."}}`))
		}))
		defer provider.Close()
		b, _ := json.Marshal(map[string]string{"base_url": provider.URL + "/v1", "api_key": "sk-valid", "model": "big-pickle"})
		req, err := http.NewRequest("POST", "/test", bytes.NewBuffer(b))
		assert.NoError(t, err)
		rr := httptest.NewRecorder()
		(&endpoints.API{}).TestProvider(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		var res map[string]interface{}
		assert.NoError(t, json.Unmarshal(rr.Body.Bytes(), &res))
		return res
	}

	for i := 0; i < 5; i++ { // the two shapes answer in either order
		assert.Equal(t, "OpenCode's free tier can only be used from within OpenCode", run(refusal)["error"])
	}
	assert.Equal(t, "Invalid API Key or unauthorized access.", run(`{}`)["error"], "an unexplained refusal is still put down to the key")
}
