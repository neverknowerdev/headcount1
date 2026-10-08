package models

import (
	"strings"
	"time"
)

const (
	TaskStatusBacklog        = "backlog"
	TaskStatusTodo           = "to-do"
	TaskStatusInProgress     = "in-progress"
	TaskStatusBlocked        = "blocked"
	TaskStatusDependsOnTask  = "depends-on-task"
	TaskStatusInReview       = "in-review"
	TaskStatusDone           = "done"
	TaskStatusFailed         = "failed"
	TaskStatusCanceled       = "canceled"
	TaskRelationDependsOn    = "depends_on"
	TaskRelationRelatedTo    = "related_to"
	DefaultTaskGitBaseBranch = "main"
)

// Task types select the workflow template a task runs through.
const (
	TaskTypeResearch = "research"
	TaskTypeCoding   = "coding"
	TaskTypeReview   = "review"
	TaskTypeGeneral  = "general"
)

// Task modes decide who drives a task: a smart model through phases
// (managed), or one executor session on the cheap tier (direct).
const (
	TaskModeManaged = "managed"
	TaskModeDirect  = "direct"
)

// Phases of a managed task. Design and test_plan exist only for coding.
const (
	TaskPhaseRefine   = "refine"
	TaskPhaseDesign   = "design"
	TaskPhaseTestPlan = "test_plan"
	TaskPhasePlan     = "plan"
	TaskPhaseExecute  = "execute"
	TaskPhaseAdjust   = "adjust"
	TaskPhaseVerify   = "verify"
)

// What a started task is waiting on. Every wait is stored here rather than
// held by a goroutine, so a restart loses nothing; the empty value means the
// task is ready for its next step.
const (
	TaskWaitNone      = ""
	TaskWaitSubtasks  = "subtasks"
	TaskWaitRun       = "run"
	TaskWaitHuman     = "human"
	TaskWaitVault     = "vault"
	TaskWaitConfig    = "config"
	TaskWaitWorkspace = "workspace"
	TaskWaitBackoff   = "backoff"
	TaskWaitOperator  = "operator"
)

// Why a task ended failed or canceled.
const (
	TaskResultReportedFailure    = "reported_failure"
	TaskResultCannotComplete     = "cannot_complete"
	TaskResultRunError           = "run_error"
	TaskResultBudgetExhausted    = "budget_exhausted"
	TaskResultPrerequisiteFailed = "prerequisite_failed"
	TaskResultStopped            = "stopped"
)

// Verdict of a review task. A review that finds problems still finishes done;
// the verdict, not the status, says whether changes are needed.
const (
	TaskVerdictApproved         = "approved"
	TaskVerdictChangesRequested = "changes_requested"
)

// IsTerminalTaskStatus reports whether a subtask in this status will not
// change again on its own: its parent no longer waits on it.
func IsTerminalTaskStatus(status string) bool {
	switch status {
	case TaskStatusDone, TaskStatusFailed, TaskStatusCanceled:
		return true
	}
	return false
}

func (task Task) EffectiveGitBaseBranch() string {
	if branch := strings.TrimSpace(task.GitBaseBranch); branch != "" {
		return branch
	}
	return DefaultTaskGitBaseBranch
}

type Task struct {
	ID                 int32    `json:"id" gorm:"primaryKey"`
	CompanyID          int32    `json:"company_id" gorm:"not null"`
	Company            Company  `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	ProjectID          *int32   `json:"project_id"`
	Project            *Project `json:"project" gorm:"foreignKey:ProjectID;constraint:OnDelete:SET NULL;"`
	SprintID           int32    `json:"sprint_id" gorm:"not null"`
	Sprint             Sprint   `json:"sprint" gorm:"foreignKey:SprintID;constraint:OnDelete:CASCADE;"`
	AgentID            *int32   `json:"agent_id"`
	Agent              *Agent   `json:"agent" gorm:"foreignKey:AgentID;constraint:OnDelete:SET NULL;"`
	ParentID           *int32   `json:"parent_id"`
	Parent             *Task    `json:"parent" gorm:"foreignKey:ParentID;constraint:OnDelete:SET NULL;"`
	Title              string   `json:"title" gorm:"not null"`
	Description        string   `json:"description"`
	RefKey             string   `json:"ref_key" gorm:"index"`
	RefinedDescription string   `json:"refined_description" gorm:"type:text;default:''"`
	// Design is the technical design a coding task's design phase produced.
	Design             string               `json:"design" gorm:"not null;default:''"`
	AcceptanceCriteria string               `json:"acceptance_criteria" gorm:"type:text;default:''"`
	TestCases          string               `json:"test_cases" gorm:"type:text;default:''"`
	Priority           string               `json:"priority" gorm:"not null;default:'Normal'"`
	Status             string               `json:"status" gorm:"not null;default:'backlog'"`
	DueDate            *time.Time           `json:"due_date"`
	IsArchived         bool                 `json:"is_archived" gorm:"not null;default:false"`
	RunID              *int32               `json:"run_id"`
	GitHubPRNumber     int                  `json:"github_pr_number"`
	GitHubPRURL        string               `json:"github_pr_url"`
	GitHubBranch       string               `json:"github_branch" gorm:"index"`
	GitBaseBranch      string               `json:"git_base_branch" gorm:"not null;default:'main'"`
	RelationSummary    *TaskRelationSummary `json:"relation_summary,omitempty" gorm:"-"`
	CreatedAt          time.Time            `json:"created_at"`
	UpdatedAt          time.Time            `json:"updated_at"`
	DoneAt             *time.Time           `json:"done_at,omitempty"`

	// Workflow state. Written only through workflow transitions.
	TaskType   string     `json:"task_type" gorm:"not null;default:'general'"`
	Mode       string     `json:"mode" gorm:"not null;default:'managed'"`
	Phase      string     `json:"phase" gorm:"not null;default:''"`
	WaitingOn  string     `json:"waiting_on" gorm:"not null;default:''"`
	WaitRef    *int32     `json:"wait_ref,omitempty"`
	WaitUntil  *time.Time `json:"wait_until,omitempty"`
	WaitDetail string     `json:"wait_detail" gorm:"not null;default:''"`
	LeaseOwner string     `json:"-" gorm:"not null;default:''"`
	LeaseUntil *time.Time `json:"-"`

	// Position in the task tree. RootTaskID is the task's own ID for a root.
	RootTaskID int32 `json:"root_task_id" gorm:"not null;default:0;index"`
	Depth      int   `json:"depth" gorm:"not null;default:0"`
	// OriginStepID is the journal step of the parent that created this task;
	// the parent waits on the children its own steps created. OriginPhase is
	// the parent's phase at that moment, and WorkflowPhase the phase of the
	// top-level task the work rolls up to in usage reports.
	OriginStepID  *int64 `json:"origin_step_id,omitempty" gorm:"index"`
	OriginPhase   string `json:"origin_phase" gorm:"not null;default:''"`
	WorkflowPhase string `json:"workflow_phase" gorm:"not null;default:''"`

	// Per-task model override; empty means the tier default for the mode.
	ProviderID   *int32 `json:"provider_id"`
	ModelGroupID *int32 `json:"model_group_id"`
	Model        string `json:"model" gorm:"not null;default:''"`

	// Outcome reported by the task's executor or decided by the workflow.
	ResultReason   string `json:"result_reason" gorm:"not null;default:''"`
	ResultSummary  string `json:"result_summary" gorm:"not null;default:''"`
	ResultDetails  string `json:"result_details" gorm:"not null;default:''"`
	ResultEvidence string `json:"result_evidence" gorm:"not null;default:''"`
	ResultVerdict  string `json:"result_verdict" gorm:"not null;default:''"`

	// Budgets consumed since the task was last started.
	SmartStepsUsed   int `json:"smart_steps_used" gorm:"not null;default:0"`
	AdjustCyclesUsed int `json:"adjust_cycles_used" gorm:"not null;default:0"`
	AttemptsUsed     int `json:"attempts_used" gorm:"not null;default:0"`
	ReviewRound      int `json:"review_round" gorm:"not null;default:0"`

	// WorkspaceOwnerTaskID is set on a root task only: the one subtask
	// currently allowed to write to the tree's shared worktree.
	WorkspaceOwnerTaskID *int32 `json:"workspace_owner_task_id,omitempty"`
}

// Description holds the user's original input, untouched; for a subtask it
// is the instructions its parent wrote. RefinedDescription is the
// specification a managed task's refinement produced.
// GitHubBranch is canonical on the root task. Subtasks copy the same value
// so every run in the task tree operates on one branch.
