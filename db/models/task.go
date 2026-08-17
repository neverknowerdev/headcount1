package models

import (
	"strings"
	"time"
)

const (
	TaskStatusBacklog        = "backlog"
	TaskStatusTodo           = "to-do"
	TaskStatusRefinement     = "refinement"
	TaskStatusInProgress     = "in-progress"
	TaskStatusBlocked        = "blocked"
	TaskStatusDependsOnTask  = "depends-on-task"
	TaskStatusInReview       = "in-review"
	TaskStatusDone           = "done"
	TaskRelationDependsOn    = "depends_on"
	TaskRelationRelatedTo    = "related_to"
	DefaultTaskGitBaseBranch = "main"
)

func (task Task) EffectiveGitBaseBranch() string {
	if branch := strings.TrimSpace(task.GitBaseBranch); branch != "" {
		return branch
	}
	return DefaultTaskGitBaseBranch
}

type Task struct {
	ID                 int32                `json:"id" gorm:"primaryKey"`
	CompanyID          int32                `json:"company_id" gorm:"not null"`
	Company            Company              `json:"company" gorm:"foreignKey:CompanyID;constraint:OnDelete:CASCADE;"`
	ProjectID          *int32               `json:"project_id"`
	Project            *Project             `json:"project" gorm:"foreignKey:ProjectID;constraint:OnDelete:SET NULL;"`
	SprintID           int32                `json:"sprint_id" gorm:"not null"`
	Sprint             Sprint               `json:"sprint" gorm:"foreignKey:SprintID;constraint:OnDelete:CASCADE;"`
	AgentID            *int32               `json:"agent_id"`
	Agent              *Agent               `json:"agent" gorm:"foreignKey:AgentID;constraint:OnDelete:SET NULL;"`
	ParentID           *int32               `json:"parent_id"`
	Parent             *Task                `json:"parent" gorm:"foreignKey:ParentID;constraint:OnDelete:SET NULL;"`
	Title              string               `json:"title" gorm:"not null"`
	Description        string               `json:"description"`
	RefKey             string               `json:"ref_key" gorm:"index"`
	RefinedDescription string               `json:"refined_description" gorm:"type:text;default:''"`
	AcceptanceCriteria string               `json:"acceptance_criteria" gorm:"type:text;default:''"`
	TestCases          string               `json:"test_cases" gorm:"type:text;default:''"`
	Priority           string               `json:"priority" gorm:"not null;default:'Normal'"`
	Status             string               `json:"status" gorm:"not null;default:'backlog'"`
	DueDate            *time.Time           `json:"due_date"`
	IsArchived         bool                 `json:"is_archived" gorm:"not null;default:false"`
	RunID              *int32               `json:"run_id"`
	OrchestratorRunID  *int32               `json:"orchestrator_run_id,omitempty" gorm:"index"`
	GitHubPRNumber     int                  `json:"github_pr_number"`
	GitHubPRURL        string               `json:"github_pr_url"`
	GitHubBranch       string               `json:"github_branch" gorm:"index"`
	GitBaseBranch      string               `json:"git_base_branch" gorm:"not null;default:'main'"`
	RelationSummary    *TaskRelationSummary `json:"relation_summary,omitempty" gorm:"-"`
	CreatedAt          time.Time            `json:"created_at"`
	UpdatedAt          time.Time            `json:"updated_at"`
}

// Description holds the user's original input, untouched. For delegated
// subtasks the owner's instructions land in RefinedDescription instead,
// shown separately in the UI.
// GitHubBranch is canonical on the root task. Subtasks copy the same value
// so every run in the task tree operates on one branch.
