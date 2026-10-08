package engine

import (
	"encoding/json"
	"fmt"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/db/models"
)

const decisionLineLimit = 280

func oneLine(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if runes := []rune(text); len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return text
}

// renderDecisionTree lays out the decisions of a task tree as indented text:
// each task, the decisions recorded on it nested by parent decision, then its
// subtasks. tasks must list parents before their children, as
// ListTaskSubtree returns them. A superseded decision is marked, not hidden:
// that an approach was replaced is part of what a reader needs to know.
func renderDecisionTree(tasks []db.Task, decisions []db.Decision) string {
	if len(tasks) == 0 {
		return ""
	}
	childrenOf := map[int32][]db.Task{}
	inTree := map[int32]bool{}
	for _, task := range tasks {
		inTree[task.ID] = true
	}
	var roots []db.Task
	for _, task := range tasks {
		if task.ParentID != nil && inTree[*task.ParentID] {
			childrenOf[*task.ParentID] = append(childrenOf[*task.ParentID], task)
		} else {
			roots = append(roots, task)
		}
	}
	byTask := map[int32][]db.Decision{}
	known := map[int64]bool{}
	superseded := map[int64]int64{}
	for _, decision := range decisions {
		byTask[decision.TaskID] = append(byTask[decision.TaskID], decision)
		known[decision.ID] = true
		if decision.SupersedesID != nil {
			superseded[*decision.SupersedesID] = decision.ID
		}
	}

	var b strings.Builder
	var writeDecisions func(list []db.Decision, parent int64, indent string)
	writeDecisions = func(list []db.Decision, parent int64, indent string) {
		for _, decision := range list {
			own := int64(0)
			// A decision whose parent is not in view hangs off its task.
			if decision.ParentDecisionID != nil && known[*decision.ParentDecisionID] {
				own = *decision.ParentDecisionID
			}
			if own != parent {
				continue
			}
			line := fmt.Sprintf("#%d [%s] %s", decision.ID, decision.Kind, oneLine(decision.Title, 120))
			if text := oneLine(decision.Decision, decisionLineLimit); text != "" && text != oneLine(decision.Title, 120) {
				line += ": " + text
			}
			if why := oneLine(decision.Rationale, decisionLineLimit); why != "" {
				line += " — because " + why
			}
			var alternatives []string
			if decision.Alternatives != "" {
				_ = json.Unmarshal([]byte(decision.Alternatives), &alternatives)
			}
			if len(alternatives) > 0 {
				line += " (rejected: " + oneLine(strings.Join(alternatives, "; "), decisionLineLimit) + ")"
			}
			if by, ok := superseded[decision.ID]; ok {
				line += fmt.Sprintf(" [superseded by #%d]", by)
			}
			b.WriteString(indent + line + "\n")
			writeDecisions(list, decision.ID, indent+"  ")
		}
	}
	var writeTask func(task db.Task, indent string)
	writeTask = func(task db.Task, indent string) {
		name := task.Title
		if task.RefKey != "" {
			name = task.RefKey + " " + name
		}
		fmt.Fprintf(&b, "%s%s [%s]\n", indent, oneLine(name, 160), task.Status)
		writeDecisions(byTask[task.ID], 0, indent+"  ")
		for _, child := range childrenOf[task.ID] {
			writeTask(child, indent+"  ")
		}
	}
	for _, root := range roots {
		writeTask(root, "")
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderExecutionState writes out subtasks in full for a smart model that
// asked to read them: the whole report, its evidence, and the checkpoints the
// executor recorded along the way.
func renderExecutionState(children []db.WorkflowChild, checkpoints []db.TaskStep, only []int32) string {
	wanted := map[int32]bool{}
	for _, id := range only {
		wanted[id] = true
	}
	byTask := map[int32][]db.TaskStep{}
	for _, checkpoint := range checkpoints {
		byTask[checkpoint.TaskID] = append(byTask[checkpoint.TaskID], checkpoint)
	}
	var b strings.Builder
	for _, child := range children {
		if len(wanted) > 0 && !wanted[child.ID] {
			continue
		}
		name := child.Title
		if child.RefKey != "" {
			name = child.RefKey + " " + name
		}
		fmt.Fprintf(&b, "Task %d: %s [%s] — %s", child.ID, name, child.TaskType, child.Status)
		if child.ResultReason != "" {
			fmt.Fprintf(&b, " (%s)", child.ResultReason)
		}
		if child.ResultVerdict != "" {
			fmt.Fprintf(&b, ", verdict: %s", child.ResultVerdict)
		}
		b.WriteString("\n")
		if child.Status == models.TaskStatusInProgress && child.WaitDetail != "" {
			fmt.Fprintf(&b, "Waiting: %s\n", child.WaitDetail)
		}
		fmt.Fprintf(&b, "Instructions:\n%s\n", strings.TrimSpace(child.Description))
		if summary := strings.TrimSpace(child.ResultSummary); summary != "" {
			fmt.Fprintf(&b, "Summary:\n%s\n", summary)
		}
		if details := strings.TrimSpace(child.ResultDetails); details != "" {
			fmt.Fprintf(&b, "Details:\n%s\n", details)
		}
		var evidence []string
		if child.ResultEvidence != "" {
			_ = json.Unmarshal([]byte(child.ResultEvidence), &evidence)
		}
		if len(evidence) > 0 {
			b.WriteString("Evidence:\n")
			for _, item := range evidence {
				b.WriteString("- " + item + "\n")
			}
		}
		if steps := byTask[child.ID]; len(steps) > 0 {
			b.WriteString("Checkpoints:\n")
			for _, step := range steps {
				b.WriteString("- " + oneLine(step.Result, 600) + "\n")
			}
		}
		b.WriteString("\n")
	}
	if b.Len() == 0 {
		return "No matching subtasks."
	}
	return strings.TrimSpace(b.String())
}
