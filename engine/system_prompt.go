package engine

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"text/template"

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
- blocked: you are stuck and need user input
- done: task is fully complete, no review needed
- refinement: you need clarification before you can start

Use write_artifact to produce structured markdown deliverables (plans, reports, specs, documentation).

Context of your work:
{{if .CompanyName}}Company: {{.CompanyName}}. {{.CompanyDescription}}{{end}}
{{if .ProjectName}}Project: {{.ProjectName}}. {{.ProjectDescription}}{{end}}
{{if .SprintName}}Sprint: {{.SprintName}}. {{.SprintDescription}}{{end}}
Working directory: {{.WorkingDirectory}}

Task name: {{.TaskName}}
Task status: {{.TaskStatus}}
Task (user input): {{.TaskDescription}}
{{if .RefinedDescription}}Refined task description:
{{.RefinedDescription}}
{{end}}{{if .AcceptanceCriteria}}Acceptance criteria:
{{.AcceptanceCriteria}}
{{end}}{{if .TestCases}}Test cases:
{{.TestCases}}
{{end}}`

type PromptData struct {
	CompanyName        string
	CompanyDescription string
	ProjectName        string
	ProjectDescription string
	SprintName         string
	SprintDescription  string
	WorkingDirectory   string
	TaskName           string
	TaskStatus         string
	TaskDescription    string
	RefinedDescription string
	AcceptanceCriteria string
	TestCases          string
}

func (b *defaultSystemPromptBuilder) Build(agent db.Agent, task db.Task) string {
	data := PromptData{
		TaskName:           task.Title,
		TaskStatus:         task.Status,
		TaskDescription:    task.Description,
		RefinedDescription: task.RefinedDescription,
		AcceptanceCriteria: task.AcceptanceCriteria,
		TestCases:          task.TestCases,
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
