package workflow

import (
	"embed"
	"fmt"
	"strings"

	"agent-orchestrator/db/models"
)

//go:embed prompts/*.md
var promptFiles embed.FS

func promptText(name string) string {
	body, err := promptFiles.ReadFile("prompts/" + name + ".md")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

// Answer is the result of one question a smart step asked.
type Answer struct {
	TaskID   int32
	Question string
	Kind     string
	// Status is the answering subtask's status; Reason says why it failed.
	Status  string
	Reason  string
	Verdict string
	Summary string
}

// SubtaskReport is one delegated piece of work as the smart model sees it.
type SubtaskReport struct {
	TaskID  int32
	RefKey  string
	Title   string
	Type    string
	Role    string
	Status  string
	Reason  string
	Verdict string
	Summary string
	// DependsOn lists the sibling task IDs this one waited for.
	DependsOn []int32
	// Handled marks work an earlier re-plan already dealt with.
	Handled bool
}

// HumanExchange is a question put to the human and their answer.
type HumanExchange struct {
	Question string
	Answer   string
}

// Inspection is the output of a read-only tool the model called earlier in
// this phase.
type Inspection struct {
	Tool   string
	Output string
}

// PromptInput is everything a smart step's prompt is built from. It is
// assembled fresh for every step; nothing is carried over from a previous
// prompt.
type PromptInput struct {
	// RolePrompt is the speaking agent's own prompt: its role line plus
	// whatever the user added.
	RolePrompt string
	Phase      string

	TaskRef     string
	TaskTitle   string
	TaskType    string
	Description string
	// Attachments names the files the human attached to the task. The smart
	// model cannot open them; an executor can.
	Attachments []string
	// ParentContext places a subtask: what the task above it is and what it
	// asked for.
	ParentContext string

	Spec             string
	DefinitionOfDone []SpecItem
	Design           string
	TestScenarios    []SpecItem

	Answers   []Answer
	Humans    []HumanExchange
	Subtasks  []SubtaskReport
	Decisions string
	// Situation says why the task is in this phase now, when that is not
	// obvious from the phase itself: what failed, what verification rejected.
	Situation   string
	Inspections []Inspection

	StepsLeft   int
	AdjustsLeft int
	// Budget caps the size of the task context in characters; zero uses
	// DefaultPromptBudget.
	Budget int
	// Condensed replaces the body of the named sections with a shorter
	// rendering of the same content, prepared because the full one did not
	// fit (see Prompt.Overflow).
	Condensed map[string]string
}

// DefaultPromptBudget is the default size of a prompt's task context, in
// characters (roughly a quarter as many tokens).
const DefaultPromptBudget = 60000

// Prompt is a composed smart-step prompt.
type Prompt struct {
	System string
	User   string
	// Truncated names the sections that did not fit their share of the budget
	// and were cut.
	Truncated []string
	// Overflow holds each cut section in full, with the room it has, so the
	// driver can have it condensed and compose again.
	Overflow []Overflow
}

// Overflow is a section that did not fit.
type Overflow struct {
	Name  string
	Title string
	Full  string
	// Limit is how many characters the section may take.
	Limit int
}

const condensedNote = "[Condensed to fit. Task numbers are kept: use the read-only tools to read any of it in full.]\n\n"

// section is one labelled block of the task context with its share of the
// budget. A weight of zero means the section is never cut.
type section struct {
	name   string
	title  string
	body   string
	weight int
}

// Compose builds the prompt for one smart step. The layout is fixed, so the
// same input always yields the same prompt: who is speaking and how the
// workflow works, the goal of the phase, then the task and everything known
// about it, most stable first.
func Compose(in PromptInput) Prompt {
	var system strings.Builder
	if role := strings.TrimSpace(in.RolePrompt); role != "" {
		system.WriteString(role + "\n\n")
	}
	system.WriteString(promptText("system"))
	if goal := promptText(in.Phase); goal != "" {
		system.WriteString("\n\n" + goal)
	}

	sections := []section{
		{name: "task", title: "Task", body: taskSection(in)},
		{name: "situation", title: "Why you are here", body: strings.TrimSpace(in.Situation)},
		{name: "spec", title: "Specification", body: strings.TrimSpace(in.Spec), weight: 3},
		{name: "definition_of_done", title: "Definition of done", body: specList(in.DefinitionOfDone)},
		{name: "design", title: "Technical design", body: strings.TrimSpace(in.Design), weight: 3},
		{name: "test_scenarios", title: "Test scenarios", body: specList(in.TestScenarios), weight: 2},
		{name: "human", title: "Answers from the human", body: humanSection(in.Humans), weight: 2},
		{name: "answers", title: "Answers to your questions", body: answerSection(in.Answers), weight: 4},
		{name: "subtasks", title: "Delegated work", body: subtaskSection(in.Subtasks), weight: 4},
		{name: "decisions", title: "Decisions so far", body: strings.TrimSpace(in.Decisions), weight: 3},
		{name: "inspections", title: "What you asked to read", body: inspectionSection(in.Inspections), weight: 4},
	}

	for i := range sections {
		if condensed := strings.TrimSpace(in.Condensed[sections[i].name]); condensed != "" && sections[i].body != "" {
			sections[i].body = condensedNote + condensed
		}
	}

	budget := in.Budget
	if budget <= 0 {
		budget = DefaultPromptBudget
	}
	overflow := fit(sections, budget)
	truncated := make([]string, 0, len(overflow))
	for _, cut := range overflow {
		truncated = append(truncated, cut.Name)
	}
	if len(truncated) == 0 {
		truncated = nil
	}

	var user strings.Builder
	for _, s := range sections {
		if s.body == "" {
			continue
		}
		fmt.Fprintf(&user, "## %s\n\n%s\n\n", s.title, s.body)
	}
	user.WriteString("## Your move\n\n")
	user.WriteString(moveLine(in))

	return Prompt{System: system.String(), User: strings.TrimSpace(user.String()), Truncated: truncated, Overflow: overflow}
}

// fit cuts sections that exceed their share of the budget and returns those
// it cut, in full. Sections that never get cut keep their size; the rest
// share what remains in proportion to their weight, and space a small section
// does not need goes to the ones that do.
func fit(sections []section, budget int) []Overflow {
	total := 0
	for _, s := range sections {
		total += len(s.body)
	}
	if total <= budget {
		return nil
	}
	remaining := budget
	flexible := map[int]bool{}
	for i, s := range sections {
		if s.weight == 0 {
			remaining -= len(s.body)
			continue
		}
		if s.body != "" {
			flexible[i] = true
		}
	}
	if remaining < 0 {
		remaining = 0
	}
	// Settle sections that fit within their share first, handing their unused
	// space to the others, until only oversized sections are left.
	for changed := true; changed; {
		changed = false
		weights := 0
		for i := range flexible {
			weights += sections[i].weight
		}
		for i := range flexible {
			share := remaining * sections[i].weight / weights
			if len(sections[i].body) <= share {
				remaining -= len(sections[i].body)
				delete(flexible, i)
				changed = true
				break
			}
		}
	}
	weights := 0
	for i := range flexible {
		weights += sections[i].weight
	}
	var cut []Overflow
	for i := range sections {
		if !flexible[i] {
			continue
		}
		share := remaining * sections[i].weight / weights
		cut = append(cut, Overflow{Name: sections[i].name, Title: sections[i].title, Full: sections[i].body, Limit: share})
		sections[i].body = truncate(sections[i].body, share)
	}
	return cut
}

const truncationNote = "\n\n[Cut to fit. Use the read-only tools to see the rest, or ask a specific question.]"

func truncate(body string, limit int) string {
	limit -= len(truncationNote)
	if limit < 0 {
		limit = 0
	}
	if len(body) <= limit {
		return body
	}
	cut := body[:limit]
	// Prefer to end on a whole line so a report is not cut mid-sentence.
	if newline := strings.LastIndexByte(cut, '\n'); newline > limit/2 {
		cut = cut[:newline]
	}
	return strings.TrimRight(cut, "\n ") + truncationNote
}

func taskSection(in PromptInput) string {
	var b strings.Builder
	title := strings.TrimSpace(in.TaskTitle)
	if in.TaskRef != "" {
		title = in.TaskRef + " — " + title
	}
	fmt.Fprintf(&b, "%s\nType: %s\n", title, in.TaskType)
	if description := strings.TrimSpace(in.Description); description != "" {
		b.WriteString("\n" + description + "\n")
	}
	if len(in.Attachments) > 0 {
		b.WriteString("\nFiles attached by the human: " + strings.Join(in.Attachments, ", ") +
			"\nYou cannot open them. Whoever you delegate work to can; ask a question about a file to learn what is in it.\n")
	}
	if parent := strings.TrimSpace(in.ParentContext); parent != "" {
		b.WriteString("\nThis is part of a larger task:\n" + parent + "\n")
	}
	return strings.TrimSpace(b.String())
}

func specList(items []SpecItem) string {
	var b strings.Builder
	for _, item := range items {
		mark := ""
		switch item.Status {
		case "passed":
			mark = " [met]"
		case "failed":
			mark = " [NOT met]"
		}
		fmt.Fprintf(&b, "%d. %s%s\n", item.ID, item.Text, mark)
		if note := strings.TrimSpace(item.Note); note != "" {
			fmt.Fprintf(&b, "   %s\n", note)
		}
	}
	return strings.TrimSpace(b.String())
}

func humanSection(exchanges []HumanExchange) string {
	var b strings.Builder
	for _, exchange := range exchanges {
		fmt.Fprintf(&b, "You asked: %s\nThey answered: %s\n\n", strings.TrimSpace(exchange.Question), strings.TrimSpace(exchange.Answer))
	}
	return strings.TrimSpace(b.String())
}

func answerSection(answers []Answer) string {
	var b strings.Builder
	for _, answer := range answers {
		fmt.Fprintf(&b, "Q (task %d): %s\n", answer.TaskID, strings.TrimSpace(answer.Question))
		switch {
		case answer.Status == models.TaskStatusDone && answer.Verdict != "":
			fmt.Fprintf(&b, "Verdict: %s\n%s\n\n", answer.Verdict, strings.TrimSpace(answer.Summary))
		case answer.Status == models.TaskStatusDone:
			fmt.Fprintf(&b, "A: %s\n\n", strings.TrimSpace(answer.Summary))
		default:
			fmt.Fprintf(&b, "NOT ANSWERED (%s): %s\n\n", outcome(answer.Status, answer.Reason), strings.TrimSpace(answer.Summary))
		}
	}
	return strings.TrimSpace(b.String())
}

func subtaskSection(subtasks []SubtaskReport) string {
	var b strings.Builder
	for _, subtask := range subtasks {
		name := subtask.Title
		if subtask.RefKey != "" {
			name = subtask.RefKey + " " + name
		}
		fmt.Fprintf(&b, "- Task %d: %s [%s", subtask.TaskID, name, subtask.Type)
		if subtask.Role != "" {
			fmt.Fprintf(&b, ", %s", subtask.Role)
		}
		fmt.Fprintf(&b, "] — %s", strings.ToUpper(outcome(subtask.Status, subtask.Reason)))
		if subtask.Verdict != "" {
			fmt.Fprintf(&b, ", verdict: %s", subtask.Verdict)
		}
		if subtask.Handled {
			b.WriteString(" (already dealt with by an earlier re-plan)")
		}
		b.WriteString("\n")
		if len(subtask.DependsOn) > 0 {
			ids := make([]string, 0, len(subtask.DependsOn))
			for _, id := range subtask.DependsOn {
				ids = append(ids, fmt.Sprintf("%d", id))
			}
			fmt.Fprintf(&b, "  after: %s\n", strings.Join(ids, ", "))
		}
		if summary := strings.TrimSpace(subtask.Summary); summary != "" {
			fmt.Fprintf(&b, "  %s\n", strings.ReplaceAll(summary, "\n", "\n  "))
		}
	}
	return strings.TrimSpace(b.String())
}

func inspectionSection(inspections []Inspection) string {
	var b strings.Builder
	for _, inspection := range inspections {
		fmt.Fprintf(&b, "Output of %s:\n%s\n\n", inspection.Tool, strings.TrimSpace(inspection.Output))
	}
	return strings.TrimSpace(b.String())
}

// outcome names a subtask's state the way a reader would say it.
func outcome(status, reason string) string {
	switch reason {
	case models.TaskResultCannotComplete:
		return "could not be completed"
	case models.TaskResultReportedFailure:
		return "failed"
	case models.TaskResultRunError:
		return "the executor crashed"
	case models.TaskResultModelError:
		return "never ran"
	case models.TaskResultBudgetExhausted:
		return "gave up"
	case models.TaskResultPrerequisiteFailed:
		return "not started: a task it depends on did not succeed"
	case models.TaskResultStopped:
		return "stopped"
	}
	return status
}

func moveLine(in PromptInput) string {
	var b strings.Builder
	b.WriteString("Answer with exactly one tool call.")
	if in.StepsLeft > 0 && in.StepsLeft <= 5 {
		fmt.Fprintf(&b, " You have %d steps left on this task; use them to finish.", in.StepsLeft)
	}
	if in.Phase == models.TaskPhaseAdjust && in.AdjustsLeft == 0 {
		b.WriteString(" This is the last re-plan allowed; if this plan cannot succeed, escalate.")
	}
	return b.String()
}
