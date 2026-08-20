package engine

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"text/template"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/agentconfig"
	"agent-orchestrator/pkg/appsettings"
	"agent-orchestrator/pkg/filesystem"
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

// loadSettings reads the app settings through the shared appsettings loader.
func loadSettings() appsettings.Settings {
	return appsettings.Load()
}

var promptTemplate = agentconfig.MustPrompt("utils/system.md")

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
	TaskRelations      string
	TaskDescription    string
	RefinedDescription string
	AcceptanceCriteria string
	TestCases          string
	CanAskHuman        bool
}

// formatSpecItems renders structured spec items as numbered lines with their
// id and verification status, so agents can reference items by id when
// calling verify_spec_items. Legacy plain-text content passes through as-is.
func formatSpecItems(raw string) string {
	items := db.ParseSpecItems(raw)
	if items == nil {
		return raw
	}
	var b bytes.Buffer
	for i, item := range items {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "%d. [%s] %s", item.ID, item.Status, item.Text)
		if item.Note != "" {
			fmt.Fprintf(&b, " — %s", item.Note)
		}
	}
	return b.String()
}

func (b *defaultSystemPromptBuilder) Build(agent db.Agent, task db.Task) string {
	data := PromptData{
		TaskName:           task.Title,
		TaskStatus:         task.Status,
		TaskDescription:    task.Description,
		RefinedDescription: task.RefinedDescription,
		AcceptanceCriteria: formatSpecItems(task.AcceptanceCriteria),
		TestCases:          formatSpecItems(task.TestCases),
		CurrentDate:        time.Now().Format("2006-01-02"),
		CanAskHuman:        agentconfig.RoleMatches(agent.RoleKey, agent.Name, "CEO"),
	}
	if summaries, err := b.q.ListTaskRelationSummaries(context.Background(), []int32{task.ID}); err == nil {
		data.TaskRelations = formatTaskRelations(summaries[task.ID])
	}

	if task.CompanyID != 0 {
		data.CompanyName = task.Company.Name
		data.CompanyDescription = task.Company.ShortName
	}

	if task.ProjectID != nil && task.Project != nil {
		data.ProjectName = task.Project.Name
		data.ProjectDescription = task.Project.Description

		settings := loadSettings()
		if task.Company.ShortName != "" {
			// The agent's actual working directory is the task's git worktree.
			fsMgr := filesystem.NewManager(settings.BasePath)
			data.WorkingDirectory = fsMgr.GetTaskWorktreePath(task.Company, task)
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

func formatTaskRelations(summary db.TaskRelationSummary) string {
	var b strings.Builder
	appendTasks := func(label string, tasks []db.TaskRelationTask) {
		if len(tasks) == 0 {
			return
		}
		fmt.Fprintf(&b, "%s:\n", label)
		for _, task := range tasks {
			ref := task.RefKey
			if ref == "" {
				ref = fmt.Sprintf("#%d", task.ID)
			}
			fmt.Fprintf(&b, "- %s — %s [%s]\n", ref, task.Title, task.Status)
		}
	}
	appendTasks("Depends on", summary.DependsOn)
	appendTasks("Blocks", summary.Blocks)
	appendTasks("Related", summary.RelatedTo)
	return strings.TrimSpace(b.String())
}
