package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"agent-orchestrator/db"
)

// responseRecorder is a custom http.ResponseWriter that captures the status code and body
type responseRecorder struct {
	http.ResponseWriter
	StatusCode int
	Body       *bytes.Buffer
}

func (rw *responseRecorder) WriteHeader(statusCode int) {
	rw.StatusCode = statusCode
	rw.ResponseWriter.WriteHeader(statusCode)
}

func (rw *responseRecorder) Write(b []byte) (int, error) {
	rw.Body.Write(b)
	return rw.ResponseWriter.Write(b)
}

// E2EMockMiddleware intercepts specific requests to mock backend processes for E2E tests.
func (s *Server) E2EMockMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Mock Provider Test Endpoint
		if strings.HasSuffix(r.URL.Path, "/providers/test") && r.Method == "POST" {
			bodyBytes, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes)) // Restore body for downstream

			var req struct {
				BaseUrl string `json:"base_url"`
			}
			json.Unmarshal(bodyBytes, &req)

			if req.BaseUrl == "e2e-mock" {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"status":        "ok",
					"provider_type": "openai",
					"log":           "Mock connection successful.",
					"url":           req.BaseUrl,
				})
				return // Short-circuit the actual handler
			}
		}

		// 2. Mock Task Engine Execution
		// For task creation, we need the actual task to be saved first so we have an ID,
		// so we let the handler run, capture the response, and then trigger our mock logic.
		if strings.HasSuffix(r.URL.Path, "/tasks") && r.Method == "POST" {
			bodyBytes, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

			var req struct {
				Title string `json:"title"`
			}
			json.Unmarshal(bodyBytes, &req)

			if req.Title == "E2E Task" || req.Title == "Write E2E Tests" {
				// Record the response to get the newly created task ID
				recorder := &responseRecorder{
					ResponseWriter: w,
					StatusCode:     http.StatusOK,
					Body:           bytes.NewBuffer(nil),
				}

				next.ServeHTTP(recorder, r)

				if recorder.StatusCode == http.StatusCreated {
					var task db.Task
					if err := json.Unmarshal(recorder.Body.Bytes(), &task); err == nil {
						// Trigger mock background execution
						go func(taskID int32) {
							time.Sleep(1 * time.Second) // Simulate work

							// Create mock comment
							comment := db.Comment{
								TaskID:     taskID,
								AuthorType: "agent",
								Content:    "I have analyzed the E2E task and completed it successfully! 🚀",
							}
							s.db.Create(&comment)
							s.hub.BroadcastEvent("comment_created", comment)

							// Create mock Run
							run := db.Run{
								TaskID:     taskID,
								AgentID:    1, // Assume first agent for mock
								Status:     "completed",
								LogContent: "Mock execution started...\nMock execution completed successfully.",
							}
							s.db.Create(&run)

							// Move task to done
							var updatedTask db.Task
							if err := s.db.First(&updatedTask, taskID).Error; err == nil {
								updatedTask.Status = "done"
								s.db.Save(&updatedTask)
								s.hub.BroadcastEvent("task_updated", updatedTask)
							}
						}(task.ID)
					}
				}
				return
			}
		}

		// Normal execution for everything else
		next.ServeHTTP(w, r)
	})
}
