// Package mempalace integrates the MemPalace memory engine
// (https://github.com/MemPalace/mempalace) as the orchestrator's memory layer.
// One palace per company; the Go side talks to it through the existing
// engine/mcp stdio client plus the mempalace CLI for lifecycle operations.
package mempalace

import (
	"fmt"
	"strings"

	"agent-orchestrator/db"
)

// Address is the single source of truth for the palace taxonomy. No other
// code composes wing/room/closet/source_file strings — every write and every
// scoped read resolves its scope through this type so the taxonomy can never
// drift between call sites.
//
// Mapping (docs/mempalace-tech-spec.md §F3):
//
//	Company        → palace (one directory per company)
//	Project        → wing  "project-<name>"
//	Agent (diary)  → wing  "agent-<name>"
//	Cross-project  → wing  "company"
//	Durable topics → rooms "general", "decisions"
//	Task           → closet tag "task-<ref-key>" (carried in content/source_file;
//	                 the MCP add_drawer API has no closet parameter)
//	Run            → source_file "tasks/<task-key>/run-<id>"
type Address struct {
	Wing       string // "project-<name>" | "agent-<name>" | "company"
	AgentWing  string // "agent-<name>" — diary wing for the executing agent
	Room       string // default room for writes ("general" or "decisions")
	Closet     string // "task-<ref-key>" — stable display key, never a DB auto-increment
	SourceFile string // "tasks/<task-key>/run-<id>"
	AddedBy    string // agent name
}

const (
	RoomGeneral   = "general"
	RoomDecisions = "decisions"
	CompanyWing   = "company"
)

// Resolve builds the Address for the current execution context. project, task,
// run and agentName may be zero/nil — the address degrades gracefully (e.g.
// tasks without a project file under the "company" wing).
//
// Keys are stable names (company shortname, project name, task ref key) —
// never DB auto-increment IDs, because backup restore reassigns those.
func Resolve(company db.Company, project *db.Project, task *db.Task, runID int32, agentName string) Address {
	addr := Address{
		Wing:    CompanyWing,
		Room:    RoomGeneral,
		AddedBy: agentName,
	}
	if project != nil && project.Name != "" {
		addr.Wing = ProjectWing(project.Name)
	}
	if agentName != "" {
		addr.AgentWing = AgentWing(agentName)
	}
	if task != nil {
		addr.Closet = TaskCloset(*task)
		if runID > 0 {
			addr.SourceFile = fmt.Sprintf("tasks/%s/run-%d", taskKey(*task), runID)
		} else {
			addr.SourceFile = fmt.Sprintf("tasks/%s", taskKey(*task))
		}
	}
	return addr
}

// ProjectWing returns the wing name for a project.
func ProjectWing(projectName string) string {
	return "project-" + SanitizeName(projectName)
}

// AgentWing returns the diary wing name for an agent.
func AgentWing(agentName string) string {
	return "agent-" + SanitizeName(agentName)
}

// TaskCloset returns the stable closet tag for a task.
func TaskCloset(task db.Task) string {
	return "task-" + taskKey(task)
}

func taskKey(task db.Task) string {
	if task.RefKey != "" {
		return SanitizeName(task.RefKey)
	}
	return fmt.Sprintf("%d", task.ID)
}

// SanitizeName maps an arbitrary string onto mempalace's safe-name charset.
// The MCP server validates wing/room names against
// ^[^\W_][\w .'-]{0,126}[^\W_]$ — i.e. word characters plus space/dot/
// apostrophe/hyphen in the middle, and the first and last character must be
// alphanumeric. Colons and slashes are rejected, so all separators become
// hyphens here. Normalization happens in this one place only.
func SanitizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "unnamed"
	}
	if len(out) > 64 {
		out = strings.Trim(out[:64], "-")
	}
	return out
}
