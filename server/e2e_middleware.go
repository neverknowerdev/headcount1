package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
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
// Only active when E2E_MODE=true environment variable is set.
func (s *Server) E2EMockMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Safety check: only run in E2E mode
		if os.Getenv("E2E_MODE") != "true" {
			next.ServeHTTP(w, r)
			return
		}

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
							TaskID:  taskID,
							AgentID: 1, // Assume first agent for mock
							Status:  "completed",
							LogContent: `[{"id":"mock-1","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","type":"info","content":"Connecting to OpenCode Server..."},{"id":"mock-2","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","type":"info","content":"Created mock session"},{"id":"mock-3","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","type":"response","content":"📥 Response from Model","fullContent":"I have analyzed the E2E task and completed it successfully! 🚀","metadata":{"responseLen":58}}]`,
						}
							s.db.Create(&run)

							// Move task to done
							var updatedTask db.Task
							if err := s.db.First(&updatedTask, taskID).Error; err == nil {
								updatedTask.Status = "in-review"
								s.db.Save(&updatedTask)
								s.hub.BroadcastEvent("task_updated", updatedTask)
							}
						}(task.ID)
					}
				}
				return
			}
		}

		// 3. Mock Task Status Update (when moving to "to-do" or "in-progress")
		// The engine is triggered on status change, but we want to mock the LLM call.
		// We intercept PUT /tasks/:id when the task has an e2e-mock provider.
		if strings.Contains(r.URL.Path, "/tasks/") && r.Method == "PUT" {
			bodyBytes, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

			var req struct {
				Status string `json:"status"`
				Title  string `json:"title"`
			}
			json.Unmarshal(bodyBytes, &req)

			// Only mock E2E test tasks, not real tasks
			isE2ETask := req.Title == "E2E Task" || req.Title == "Write E2E Tests"
			if !isE2ETask {
				// Check existing task title from DB
				parts := strings.Split(r.URL.Path, "/")
				if len(parts) > 0 {
					taskIDStr := parts[len(parts)-1]
					var existingTask db.Task
					if err := s.db.First(&existingTask, taskIDStr).Error; err == nil {
						isE2ETask = existingTask.Title == "E2E Task" || existingTask.Title == "Write E2E Tests"
					}
				}
			}

			if (req.Status == "to-do" || req.Status == "in-progress") && isE2ETask {
				// Let the handler run (it will trigger the engine)
				recorder := &responseRecorder{
					ResponseWriter: w,
					StatusCode:     http.StatusOK,
					Body:           bytes.NewBuffer(nil),
				}
				next.ServeHTTP(recorder, r)

				// Write the response back to the client
				for k, vv := range recorder.Header() {
					for _, v := range vv {
						w.Header().Add(k, v)
					}
				}
				w.WriteHeader(recorder.StatusCode)
				w.Write(recorder.Body.Bytes())

				// The engine will try to call the LLM provider in a goroutine.
				// We need to mock that call by creating the expected agent response.
				if recorder.StatusCode == http.StatusOK || recorder.StatusCode == http.StatusCreated {
					var task db.Task
					if err := json.Unmarshal(recorder.Body.Bytes(), &task); err == nil {
						go func(taskID int32) {
							time.Sleep(100 * time.Millisecond) // Wait for engine to start

							// Check if a run was already created by the engine
							var existingRun db.Run
							for i := 0; i < 50; i++ {
								if err := s.db.Where("task_id = ? AND status = ?", taskID, "running").Order("id desc").First(&existingRun).Error; err == nil {
									break
								}
								time.Sleep(100 * time.Millisecond)
							}
							if existingRun.ID == 0 {
								existingRun = db.Run{
									TaskID: taskID,
									AgentID: 1,
									Status: "running",
								}
								s.db.Create(&existingRun)
							}
							if existingRun.ID != 0 {
								time.Sleep(500 * time.Millisecond)
								// Engine created a run, mock the LLM response
								comment := db.Comment{
									TaskID:     taskID,
									AuthorType: "agent",
									Content:    "I have analyzed the E2E task and completed it successfully! 🚀",
								}
								s.db.Create(&comment)
								s.hub.BroadcastEvent("comment_created", comment)

								// Update the run to completed
								existingRun.Status = "completed"
								existingRun.LogContent = `[{"id":"mock-1","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","type":"info","content":"Connecting to OpenCode Server..."},{"id":"mock-2","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","type":"info","content":"Created mock session"},{"id":"mock-3","timestamp":"` + time.Now().UTC().Format(time.RFC3339) + `","type":"response","content":"📥 Response from Model","fullContent":"I have analyzed the E2E task and completed it successfully! 🚀","metadata":{"responseLen":58}}]`
								s.db.Save(&existingRun)
								s.hub.BroadcastEvent("run_ended", map[string]interface{}{"run_id": existingRun.ID, "status": "completed"})

								// Move task to done
								var updatedTask db.Task
								if err := s.db.First(&updatedTask, taskID).Error; err == nil {
									updatedTask.Status = "in-review"
									s.db.Save(&updatedTask)
									s.hub.BroadcastEvent("task_updated", updatedTask)
								}
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
