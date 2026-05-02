package db

import (
	"context"

	"gorm.io/gorm"
)

type Querier interface {
	CreateCompany(ctx context.Context, name string) (Company, error)
	ListCompanies(ctx context.Context) ([]Company, error)
	CreateProject(ctx context.Context, p Project) (Project, error)
	ListProjectsByCompany(ctx context.Context, companyID int32) ([]Project, error)
	CreateAgent(ctx context.Context, a Agent) (Agent, error)
	ListAgentsByCompany(ctx context.Context, companyID int32) ([]Agent, error)
	GetAgent(ctx context.Context, id int32) (Agent, error)
	CreateTask(ctx context.Context, t Task) (Task, error)
	UpdateTask(ctx context.Context, t Task) (Task, error)
	GetTask(ctx context.Context, id int32) (Task, error)
	CreateComment(ctx context.Context, c Comment) (Comment, error)
	ListCommentsByTask(ctx context.Context, taskID int32) ([]Comment, error)
	CreateAttachment(ctx context.Context, a Attachment) (Attachment, error)
	ListAttachmentsByTask(ctx context.Context, taskID int32) ([]Attachment, error)
	CreateRun(ctx context.Context, r Run) (Run, error)
	UpdateRunLog(ctx context.Context, id int32, content string, status string) error
	UpdateRunSession(ctx context.Context, id int32, sessionID string) error
	GetRun(ctx context.Context, id int32) (Run, error)
	GetRunBySessionID(ctx context.Context, sessionID string) (Run, error)
	CreateLLMProvider(ctx context.Context, p LLMProvider) (LLMProvider, error)
	CreateSkill(ctx context.Context, s Skill) (Skill, error)
	GetLLMProvider(ctx context.Context, id int32) (LLMProvider, error)
	ListLLMProviders(ctx context.Context) ([]LLMProvider, error)
	CreateSprint(ctx context.Context, s Sprint) (Sprint, error)
	ListSprintsByCompany(ctx context.Context, companyID int32) ([]Sprint, error)
	CreateProxyRequestLog(ctx context.Context, p ProxyRequestLog) (ProxyRequestLog, error)
	UpdateAgent(ctx context.Context, a Agent) (Agent, error)
	DeleteLLMProvider(ctx context.Context, id int32) error
	UpdateLLMProvider(ctx context.Context, p LLMProvider) (LLMProvider, error)
}

type Queries struct {
	db *gorm.DB
}

func New(db *gorm.DB) *Queries {
	return &Queries{db: db}
}

func (q *Queries) CreateCompany(ctx context.Context, name string) (Company, error) {
	c := Company{Name: name}
	err := q.db.WithContext(ctx).Create(&c).Error
	return c, err
}

func (q *Queries) ListCompanies(ctx context.Context) ([]Company, error) {
	var c []Company
	err := q.db.WithContext(ctx).Order("id").Find(&c).Error
	return c, err
}

func (q *Queries) CreateProject(ctx context.Context, p Project) (Project, error) {
	err := q.db.WithContext(ctx).Create(&p).Error
	return p, err
}

func (q *Queries) ListProjectsByCompany(ctx context.Context, companyID int32) ([]Project, error) {
	var p []Project
	err := q.db.WithContext(ctx).Where("company_id = ?", companyID).Order("id").Find(&p).Error
	return p, err
}

func (q *Queries) CreateAgent(ctx context.Context, a Agent) (Agent, error) {
	err := q.db.WithContext(ctx).Create(&a).Error
	return a, err
}

func (q *Queries) ListAgentsByCompany(ctx context.Context, companyID int32) ([]Agent, error) {
	var a []Agent
	err := q.db.WithContext(ctx).Where("company_id = ?", companyID).Order("id").Find(&a).Error
	return a, err
}

func (q *Queries) GetAgent(ctx context.Context, id int32) (Agent, error) {
	var a Agent
	err := q.db.WithContext(ctx).First(&a, id).Error
	return a, err
}

func (q *Queries) CreateTask(ctx context.Context, t Task) (Task, error) {
	err := q.db.WithContext(ctx).Create(&t).Error
	return t, err
}

func (q *Queries) UpdateTask(ctx context.Context, t Task) (Task, error) {
	err := q.db.WithContext(ctx).Save(&t).Error
	return t, err
}

func (q *Queries) GetTask(ctx context.Context, id int32) (Task, error) {
	var t Task
	err := q.db.WithContext(ctx).First(&t, id).Error
	return t, err
}

func (q *Queries) CreateComment(ctx context.Context, c Comment) (Comment, error) {
	err := q.db.WithContext(ctx).Create(&c).Error
	return c, err
}

func (q *Queries) ListCommentsByTask(ctx context.Context, taskID int32) ([]Comment, error) {
	var c []Comment
	err := q.db.WithContext(ctx).Where("task_id = ?", taskID).Order("created_at asc").Find(&c).Error
	return c, err
}

func (q *Queries) CreateAttachment(ctx context.Context, a Attachment) (Attachment, error) {
	err := q.db.WithContext(ctx).Create(&a).Error
	return a, err
}

func (q *Queries) ListAttachmentsByTask(ctx context.Context, taskID int32) ([]Attachment, error) {
	var a []Attachment
	err := q.db.WithContext(ctx).Where("task_id = ?", taskID).Order("created_at asc").Find(&a).Error
	return a, err
}

func (q *Queries) CreateRun(ctx context.Context, r Run) (Run, error) {
	err := q.db.WithContext(ctx).Create(&r).Error
	return r, err
}

func (q *Queries) UpdateRunLog(ctx context.Context, id int32, content string, status string) error {
	var r Run
	err := q.db.WithContext(ctx).First(&r, id).Error
	if err != nil {
		return err
	}
	r.LogContent = content
	r.Status = status
	if status == "completed" || status == "failed" {
		now := gorm.Expr("CURRENT_TIMESTAMP")
		return q.db.WithContext(ctx).Model(&r).Updates(map[string]interface{}{"log_content": content, "status": status, "ended_at": now}).Error
	}
	return q.db.WithContext(ctx).Save(&r).Error
}

func (q *Queries) UpdateRunSession(ctx context.Context, id int32, sessionID string) error {
	return q.db.WithContext(ctx).Model(&Run{}).Where("id = ?", id).Update("session_id", sessionID).Error
}

func (q *Queries) GetRun(ctx context.Context, id int32) (Run, error) {
	var r Run
	err := q.db.WithContext(ctx).First(&r, id).Error
	return r, err
}

func (q *Queries) GetRunBySessionID(ctx context.Context, sessionID string) (Run, error) {
	var r Run
	err := q.db.WithContext(ctx).Where("session_id = ?", sessionID).First(&r).Error
	return r, err
}

func (q *Queries) CreateLLMProvider(ctx context.Context, p LLMProvider) (LLMProvider, error) {
	err := q.db.WithContext(ctx).Create(&p).Error
	return p, err
}

func (q *Queries) CreateSkill(ctx context.Context, s Skill) (Skill, error) {
	err := q.db.WithContext(ctx).Create(&s).Error
	return s, err
}

func (q *Queries) GetLLMProvider(ctx context.Context, id int32) (LLMProvider, error) {
	var p LLMProvider
	err := q.db.WithContext(ctx).First(&p, id).Error
	return p, err
}

func (q *Queries) ListLLMProviders(ctx context.Context) ([]LLMProvider, error) {
	var p []LLMProvider
	err := q.db.WithContext(ctx).Order("id").Find(&p).Error
	return p, err
}

func (q *Queries) CreateSprint(ctx context.Context, s Sprint) (Sprint, error) {
	err := q.db.WithContext(ctx).Create(&s).Error
	return s, err
}

func (q *Queries) ListSprintsByCompany(ctx context.Context, companyID int32) ([]Sprint, error) {
	var s []Sprint
	err := q.db.WithContext(ctx).Where("company_id = ?", companyID).Order("id").Find(&s).Error
	return s, err
}

func (q *Queries) CreateProxyRequestLog(ctx context.Context, p ProxyRequestLog) (ProxyRequestLog, error) {
	err := q.db.WithContext(ctx).Create(&p).Error
	return p, err
}

func (q *Queries) UpdateAgent(ctx context.Context, a Agent) (Agent, error) {
	err := q.db.WithContext(ctx).Save(&a).Error
	return a, err
}

func (q *Queries) DeleteLLMProvider(ctx context.Context, id int32) error {
	return q.db.WithContext(ctx).Delete(&LLMProvider{}, id).Error
}

func (q *Queries) UpdateLLMProvider(ctx context.Context, p LLMProvider) (LLMProvider, error) {
	err := q.db.WithContext(ctx).Save(&p).Error
	return p, err
}
