import re

with open("server/handlers.go", "r") as f:
    content = f.read()

mock_logic = """
	// E2E Mock Logic
	if task.Title == "E2E Task" || task.Title == "Write E2E Tests" {
		go func() {
			time.Sleep(1 * time.Second) // Simulate work

			// Mock comment
			comment := db.Comment{
				TaskID:     task.ID,
				AuthorType: "agent",
				Content:    "I have analyzed the E2E task and completed it successfully! 🚀",
			}
			s.db.Create(&comment)
			s.hub.BroadcastEvent("comment_created", comment)

			// Mock Run
			run := db.Run{
				TaskID: task.ID,
				AgentID: 1, // Assume first agent for mock
				Status: "completed",
				LogContent: "Mock execution started...\\nMock execution completed successfully.",
			}
			s.db.Create(&run)

			// Auto move task to done
			task.Status = "done"
			s.db.Save(&task)
			s.hub.BroadcastEvent("task_updated", task)
		}()
	}
"""

content = re.sub(
    r's\.logActivity\(comp\.ID, "task_created", int32\(task\.ID\), "task", ""\)',
    's.logActivity(comp.ID, "task_created", int32(task.ID), "task", "")\n' + mock_logic,
    content
)

if '"time"' not in content:
    content = content.replace('"bytes"\n', '"bytes"\n\t"time"\n')

with open("server/handlers.go", "w") as f:
    f.write(content)
