package engine

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"text/template"
	"time"

	"agent-orchestrator/db"
	"gopkg.in/yaml.v3"
)

type SystemPromptBuilder interface {
	Build(agent db.Agent, task db.Task) string
}

type defaultSystemPromptBuilder struct {
	q *db.Queries
}

func NewSystemPromptBuilder(q *db.Queries) SystemPromptBuilder {
	return &defaultSystemPromptBuilder{q: q}
}

type Settings struct {
	BasePath         string   `yaml:"base_path"`
	WorkspaceFolders []string `yaml:"workspace_folders"`
}

func loadSettings() Settings {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return Settings{BasePath: db.PaperclipHome()}
	}
	settingsPath := filepath.Join(homeDir, ".paperclip2_settings.yaml")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return Settings{BasePath: db.PaperclipHome()}
	}

	var settings Settings
	if err := yaml.Unmarshal(data, &settings); err != nil {
		return Settings{BasePath: db.PaperclipHome()}
	}

	if settings.BasePath == "" {
		settings.BasePath = db.PaperclipHome()
	}
	return settings
}

const promptTemplate = `You are an agent that works on tasks. Implement the task on your own; ask the user only when genuinely blocked.

At the end of every run you MUST call finish_task — there are no exceptions:
- in-review: work is done, ready for human review
- blocked: you are stuck, cannot verify what was asked, or need user input — never report success you did not verify
- done: task is fully complete, no review needed
- refinement: you need clarification before you can start
Put the full handoff (findings, decisions, artifact filenames, caveats) into finish_task's result_details — other agents read it via expand_run_result.

Deliverables (plans, reports, specs, documentation) are ARTIFACTS: write them with write_artifact, discover existing ones with list_artifacts, and read them with read_artifact. Artifacts are shared across the whole task tree — always check list_artifacts before re-deriving work another agent may already have produced. Never paste a full document into a chat message when it exists as an artifact; reference its filename instead.

Your file tools are sandboxed to the working directory below. Absolute paths outside it are inaccessible; explore code through the codegraph tools when available.

Context of your work:
Current date: {{.CurrentDate}}
{{if .CompanyName}}Company: {{.CompanyName}}. {{.CompanyDescription}}{{end}}
{{if .ProjectName}}Project: {{.ProjectName}}. {{.ProjectDescription}}{{end}}
{{if .SprintName}}Sprint: {{.SprintName}}. {{.SprintDescription}}{{end}}
Working directory: {{.WorkingDirectory}}

Task name: {{.TaskName}}
Task status: {{.TaskStatus}}
Task (user input): {{.TaskDescription}}
{{if .RefinedDescription}}Refined description: {{.RefinedDescription}}{{end}}
{{if .SpecItems}}
Acceptance criteria / test cases (spec items — verify with verify_spec_items, citing evidence):
{{.SpecItems}}{{end}}`

type PromptData struct {
	CompanyName        string
	CompanyDescription string
	ProjectName        string
	ProjectDescription string
	SprintName         string
	SprintDescription  string
	WorkingDirectory   string
	CurrentDate        string
	TaskName           string
	TaskStatus         string
	TaskDescription    string
	RefinedDescription string
	SpecItems          string
}

func (b *defaultSystemPromptBuilder) Build(agent db.Agent, task db.Task) string {
	data := PromptData{
		TaskName:           task.Title,
		TaskStatus:         task.Status,
		TaskDescription:    task.Description,
		RefinedDescription: task.RefinedDescription,
		CurrentDate:        time.Now().Format("2006-01-02"),
	}

	// Spec items live on the root task of the subtask tree so every agent in
	// the tree sees the same checklist (with IDs for verify_spec_items).
	if b.q != nil {
		root, err := b.q.GetRootTask(context.Background(), task.ID)
		if err == nil {
			if root.RefinedDescription != "" && data.RefinedDescription == "" {
				data.RefinedDescription = root.RefinedDescription
			}
			if items, iErr := b.q.ListSpecItemsByTask(context.Background(), root.ID); iErr == nil && len(items) > 0 {
				var sb bytes.Buffer
				for _, it := range items {
					line := fmt.Sprintf("- [id %d] (%s, %s) %s", it.ID, it.Kind, it.Status, it.Text)
					if it.Note != "" {
						line += " — " + it.Note
					}
					sb.WriteString(line + "\n")
				}
				data.SpecItems = sb.String()
			}
		}
	}

	if task.CompanyID != 0 {
		data.CompanyName = task.Company.Name
		data.CompanyDescription = task.Company.ShortName
	}

	if task.ProjectID != nil && task.Project != nil {
		data.ProjectName = task.Project.Name
		data.ProjectDescription = task.Project.Description

		settings := loadSettings()
		if task.Company.ShortName != "" && task.Project.WorkspaceFolder != "" {
			data.WorkingDirectory = settings.BasePath + "/" + task.Company.ShortName + "/" + task.Project.WorkspaceFolder
		}
	}

	if task.SprintID != 0 {
		data.SprintName = task.Sprint.Name
		data.SprintDescription = task.Sprint.Goal
	}

	tmpl, err := template.New("prompt").Parse(promptTemplate)
	if err != nil {
		log.Printf("Failed to parse system prompt template: %v", err)
		return ""
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, data)
	if err != nil {
		log.Printf("Failed to execute system prompt template: %v", err)
		return ""
	}

	return buf.String()
}
