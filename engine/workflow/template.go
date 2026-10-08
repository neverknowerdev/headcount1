package workflow

import "agent-orchestrator/db/models"

// Built-in roles the templates refer to by name. A role is only a line of
// prompt; when a company has no agent for one, the task's own agent speaks.
const (
	RoleCTO    = "CTO"
	RoleQALead = "QA Lead"
	RoleCoder  = "Coder"
	RoleQA     = "QA"
)

// Template is the workflow of one task type: the phases a managed task goes
// through, who speaks in each, and what the engine adds on its own.
type Template struct {
	TaskType string
	// Phases is the forward path, in order. Adjust is not listed: it is
	// entered from execute or verify when something failed.
	Phases []string
	// Roles names the role that speaks in a phase. A phase not listed is
	// spoken by the task's own agent.
	Roles map[string]string
	// ReviewImplementation makes the engine attach a review to every coding
	// subtask and run fix-and-review-again rounds itself.
	ReviewImplementation bool
	// ExecutorRole and ReviewerRole are the default roles of subtasks whose
	// planner named none.
	ExecutorRole string
	ReviewerRole string
}

var standardPhases = []string{models.TaskPhaseRefine, models.TaskPhasePlan, models.TaskPhaseExecute, models.TaskPhaseVerify}

var templates = map[string]Template{
	models.TaskTypeResearch: {TaskType: models.TaskTypeResearch, Phases: standardPhases},
	models.TaskTypeReview:   {TaskType: models.TaskTypeReview, Phases: standardPhases},
	models.TaskTypeGeneral:  {TaskType: models.TaskTypeGeneral, Phases: standardPhases},
	models.TaskTypeCoding: {
		TaskType: models.TaskTypeCoding,
		Phases: []string{
			models.TaskPhaseRefine, models.TaskPhaseDesign, models.TaskPhaseTestPlan,
			models.TaskPhasePlan, models.TaskPhaseExecute, models.TaskPhaseVerify,
		},
		Roles: map[string]string{
			models.TaskPhaseDesign:   RoleCTO,
			models.TaskPhaseTestPlan: RoleQALead,
			models.TaskPhasePlan:     RoleCTO,
			models.TaskPhaseAdjust:   RoleCTO,
		},
		ReviewImplementation: true,
		ExecutorRole:         RoleCoder,
		ReviewerRole:         RoleQA,
	},
}

// TemplateFor returns the workflow of a task type; an unknown type runs the
// general workflow.
func TemplateFor(taskType string) Template {
	if template, ok := templates[taskType]; ok {
		return template
	}
	return templates[models.TaskTypeGeneral]
}

// First is the phase a managed task starts in.
func (t Template) First() string { return t.Phases[0] }

// After returns the phase that follows phase on the forward path. Adjust and
// the last phase have no successor.
func (t Template) After(phase string) (string, bool) {
	for i, name := range t.Phases {
		if name == phase && i+1 < len(t.Phases) {
			return t.Phases[i+1], true
		}
	}
	return "", false
}

// RoleFor is the role that speaks in a phase, or "" for the task's own agent.
func (t Template) RoleFor(phase string) string { return t.Roles[phase] }

// WritesWorkspace reports whether an executor of this task type changes files
// in the tree's shared worktree. Such executors run one at a time per tree;
// the others get a scratch directory and run in parallel.
func WritesWorkspace(taskType string) bool {
	return taskType == models.TaskTypeCoding || taskType == models.TaskTypeGeneral
}
