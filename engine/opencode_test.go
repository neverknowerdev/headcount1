package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestOpenCodeServerDirect(t *testing.T) {
	baseURL := "http://127.0.0.1:36000"

	// 1. Create session
	sessionBody, _ := json.Marshal(map[string]interface{}{
		"title": "test integration",
	})
	sessionResp, err := http.Post(baseURL+"/session", "application/json", bytes.NewBuffer(sessionBody))
	if err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	defer sessionResp.Body.Close()

	var sessionData struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(sessionResp.Body).Decode(&sessionData); err != nil {
		t.Fatalf("Failed to decode session: %v", err)
	}
	t.Logf("Created session: %s", sessionData.ID)

	// 2. Send message with model as object
	msgBody := map[string]interface{}{
		"parts": []map[string]interface{}{
			{
				"type": "text",
				"text": "What is 2+2? Reply with just the number.",
			},
		},
		"model": map[string]interface{}{
			"modelID":    "mimo-v2.5-pro",
			"providerID": "opencode-go",
		},
	}
	msgBytes, _ := json.Marshal(msgBody)
	t.Logf("Request body: %s", string(msgBytes))

	client := &http.Client{Timeout: 2 * time.Minute}
	msgResp, err := client.Post(
		fmt.Sprintf("%s/session/%s/message", baseURL, sessionData.ID),
		"application/json",
		bytes.NewBuffer(msgBytes),
	)
	if err != nil {
		t.Fatalf("Failed to send message: %v", err)
	}
	defer msgResp.Body.Close()

	respBytes, _ := io.ReadAll(msgResp.Body)
	t.Logf("Response status: %d", msgResp.StatusCode)
	t.Logf("Response length: %d", len(respBytes))

	if msgResp.StatusCode != http.StatusOK {
		t.Fatalf("Expected 200, got %d: %s", msgResp.StatusCode, string(respBytes))
	}

	// 3. Parse response and extract text
	var rawMsg map[string]interface{}
	if err := json.Unmarshal(respBytes, &rawMsg); err != nil {
		t.Fatalf("Failed to parse response: %v", err)
	}

	parts, ok := rawMsg["parts"].([]interface{})
	if !ok {
		t.Fatal("No parts in response")
	}

	var texts []string
	for _, p := range parts {
		if part, ok := p.(map[string]interface{}); ok {
			if text, ok := part["text"].(string); ok && text != "" {
				texts = append(texts, fmt.Sprintf("[%s]: %s", part["type"], text))
			}
		}
	}

	if len(texts) == 0 {
		t.Fatal("No text found in response parts")
	}

	t.Logf("Extracted texts:")
	for _, text := range texts {
		t.Logf("  %s", text)
	}
}
