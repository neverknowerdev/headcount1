package server

import (
	"agent-orchestrator/server/controllers"
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
