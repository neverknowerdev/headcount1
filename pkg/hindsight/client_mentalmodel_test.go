package hindsight

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestClientMentalModelCRUDAndTags exercises the Client methods backing the
// Memory UI's mental-model create/edit/history/tags features against a tiny
// stub that mirrors the real Hindsight response shapes.
func TestClientMentalModelCRUDAndTags(t *testing.T) {
	var lastPatch map[string]interface{}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/default/banks/company-1/mental-models", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method %s", r.Method)
		}
		var body map[string]interface{}
		json.NewDecoder(r.Body).Decode(&body)
		if body["name"] != "Custom insight" {
			t.Errorf("expected name to round-trip, got %v", body["name"])
		}
		json.NewEncoder(w).Encode(map[string]string{"mental_model_id": "custom-insight", "operation_id": "op-1"})
	})
	mux.HandleFunc("/v1/default/banks/company-1/mental-models/custom-insight", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Fatalf("unexpected method %s", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&lastPatch)
		json.NewEncoder(w).Encode(map[string]interface{}{"id": "custom-insight", "name": lastPatch["name"]})
	})
	mux.HandleFunc("/v1/default/banks/company-1/mental-models/custom-insight/history", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"items": []map[string]string{
				{"content": "v2", "refreshed_at": "2024-02-01T00:00:00Z"},
				{"content": "v1", "refreshed_at": "2024-01-01T00:00:00Z"},
			},
		})
	})
	mux.HandleFunc("/v1/default/banks/company-1/tags", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"items": []map[string]interface{}{{"tag": "project:1", "count": 3}},
			"total": 1, "limit": 100, "offset": 0,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(srv.URL)
	ctx := context.Background()

	id, err := c.CreateMentalModelRaw(ctx, "company-1", map[string]interface{}{
		"id": "custom-insight", "name": "Custom insight", "source_query": "What matters?",
	})
	if err != nil {
		t.Fatalf("CreateMentalModelRaw: %v", err)
	}
	var createResp struct {
		MentalModelID string `json:"mental_model_id"`
	}
	json.Unmarshal(id, &createResp)
	if createResp.MentalModelID != "custom-insight" {
		t.Errorf("expected mental_model_id custom-insight, got %q", createResp.MentalModelID)
	}

	if _, err := c.UpdateMentalModel(ctx, "company-1", "custom-insight", map[string]interface{}{"name": "Renamed"}); err != nil {
		t.Fatalf("UpdateMentalModel: %v", err)
	}
	if lastPatch["name"] != "Renamed" {
		t.Errorf("expected PATCH body to carry the new name, got %v", lastPatch)
	}

	hist, err := c.GetMentalModelHistoryRaw(ctx, "company-1", "custom-insight")
	if err != nil {
		t.Fatalf("GetMentalModelHistoryRaw: %v", err)
	}
	var histResp struct {
		Items []map[string]string `json:"items"`
	}
	json.Unmarshal(hist, &histResp)
	if len(histResp.Items) != 2 || histResp.Items[0]["content"] != "v2" {
		t.Errorf("expected 2 history items, most recent first, got %+v", histResp.Items)
	}

	tags, err := c.ListTagsRaw(ctx, "company-1")
	if err != nil {
		t.Fatalf("ListTagsRaw: %v", err)
	}
	var tagsResp struct {
		Items []map[string]interface{} `json:"items"`
	}
	json.Unmarshal(tags, &tagsResp)
	if len(tagsResp.Items) != 1 || tagsResp.Items[0]["tag"] != "project:1" {
		t.Errorf("expected tag project:1, got %+v", tagsResp.Items)
	}
}
