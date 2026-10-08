package workflow

import (
	"fmt"
	"strings"

	"agent-orchestrator/db/models"
)

// PrerequisiteResult is the outcome of a task the executor's task follows:
// the implementation a review judges, the review a fix answers.
type PrerequisiteResult struct {
	Title   string
	Type    string
	Status  string
	Verdict string
	Summary string
	Details string
}

// ExecutorInput is what an executor's opening prompt is built from.
type ExecutorInput struct {
	RolePrompt   string
	TaskRef      string
	TaskTitle    string
	TaskType     string
	Instructions string
	// ParentContext says what larger task this one is part of.
	ParentContext string
	Prerequisites []PrerequisiteResult
	// Attempt is this session's number among the tries at the task. Earlier
	// tries left EarlierProgress and EarlierRecords behind.
	Attempt         int
	EarlierProgress []string
	EarlierRecords  string
	// Environment describes the working directory and what else is readable.
	Environment string
}

// ComposeExecutor builds the opening prompt of an executor session: the
// system prompt that says how to work, and the task as its first message.
func ComposeExecutor(in ExecutorInput) (system, user string) {
	var s strings.Builder
	if role := strings.TrimSpace(in.RolePrompt); role != "" {
		s.WriteString(role + "\n\n")
	}
	s.WriteString(promptText("executor"))
	note := promptText("executor_" + in.TaskType)
	if note == "" {
		note = promptText("executor_" + models.TaskTypeGeneral)
	}
	s.WriteString("\n\n" + note)
	if environment := strings.TrimSpace(in.Environment); environment != "" {
		s.WriteString("\n\n" + environment)
	}

	var u strings.Builder
	title := strings.TrimSpace(in.TaskTitle)
	if in.TaskRef != "" {
		title = in.TaskRef + " — " + title
	}
	fmt.Fprintf(&u, "## Your task\n\n%s\n\n%s\n", title, strings.TrimSpace(in.Instructions))
	if parent := strings.TrimSpace(in.ParentContext); parent != "" {
		fmt.Fprintf(&u, "\n## What this is part of\n\n%s\n", parent)
	}
	if len(in.Prerequisites) > 0 {
		u.WriteString("\n## Results of the tasks this one follows\n")
		for _, result := range in.Prerequisites {
			fmt.Fprintf(&u, "\n### %s [%s] — %s", result.Title, result.Type, result.Status)
			if result.Verdict != "" {
				fmt.Fprintf(&u, ", verdict: %s", result.Verdict)
			}
			u.WriteString("\n")
			if summary := strings.TrimSpace(result.Summary); summary != "" {
				u.WriteString(summary + "\n")
			}
			if details := strings.TrimSpace(result.Details); details != "" {
				u.WriteString("\n" + details + "\n")
			}
		}
	}
	if in.Attempt > 1 {
		fmt.Fprintf(&u, "\n## Earlier attempt\n\nThis is attempt %d. An earlier session worked on this task and did not finish it. Do not start over blindly, and do not repeat what is recorded as a dead end.\n", in.Attempt)
		if len(in.EarlierProgress) > 0 {
			u.WriteString("\nWhere it got to:\n")
			for _, progress := range in.EarlierProgress {
				u.WriteString("- " + strings.TrimSpace(progress) + "\n")
			}
		}
		if records := strings.TrimSpace(in.EarlierRecords); records != "" {
			u.WriteString("\nWhat it recorded:\n" + records + "\n")
		}
	}
	return s.String(), strings.TrimSpace(u.String())
}
