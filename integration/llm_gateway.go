package integration

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"agent-orchestrator/db"
)

type LLMGateway struct {
	q *db.Queries
}

func NewLLMGateway(database *sql.DB) *LLMGateway {
	return &LLMGateway{
		q: db.New(database),
	}
}

func (g *LLMGateway) Mount(r chi.Router) {
	r.Post("/v1/chat/completions", g.proxyChatCompletions)
}

func (g *LLMGateway) proxyChatCompletions(w http.ResponseWriter, r *http.Request) {
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}
	r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	var reqPayload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(bodyBytes, &reqPayload); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	providerIDStr := r.Header.Get("X-Provider-ID")
	if providerIDStr == "" {
		http.Error(w, "X-Provider-ID header missing", http.StatusBadRequest)
		return
	}

	var providerID int
	fmt.Sscanf(providerIDStr, "%d", &providerID)

	provider, err := g.q.GetLLMProvider(r.Context(), int32(providerID))
	if err != nil {
		http.Error(w, "Provider not found", http.StatusNotFound)
		return
	}

	proxyReq, err := http.NewRequest(r.Method, provider.BaseUrl+"/v1/chat/completions", bytes.NewBuffer(bodyBytes))
	if err != nil {
		http.Error(w, "Failed to create proxy request", http.StatusInternalServerError)
		return
	}

	for k, vv := range r.Header {
		for _, v := range vv {
			proxyReq.Header.Add(k, v)
		}
	}
	proxyReq.Header.Set("Authorization", "Bearer "+provider.ApiKey)

	client := &http.Client{}
	resp, err := client.Do(proxyReq)
	if err != nil {
		http.Error(w, "Failed to contact provider", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
