package db

import "context"

func (q *Queries) CreateProject(ctx context.Context, p Project) (Project, error) {
	err := q.db.WithContext(ctx).Create(&p).Error
	return p, err
}

func (q *Queries) GetProject(ctx context.Context, id int32) (Project, error) {
	var p Project
	err := q.db.WithContext(ctx).Preload("Company").First(&p, id).Error
	return p, err
}

func (q *Queries) UpdateProject(ctx context.Context, p Project) (Project, error) {
	err := q.db.WithContext(ctx).Save(&p).Error
	return p, err
}

func (q *Queries) ListProjectsByCompany(ctx context.Context, companyID int32) ([]Project, error) {
	var p []Project
	err := q.db.WithContext(ctx).Where("company_id = ?", companyID).Order("id").Find(&p).Error
	return p, err
}

// ListProjectsPendingMempalaceInit returns every project that has not yet had
// `mempalace init` run for its workspace, across all companies. Used by the
// startup sweep so projects created before this flag existed (or whose init
// previously failed) get retried.
func (q *Queries) ListProjectsPendingMempalaceInit(ctx context.Context) ([]Project, error) {
	var p []Project
	err := q.db.WithContext(ctx).Preload("Company").Where("mempalace_init_done = ?", false).Order("id").Find(&p).Error
	return p, err
}

// SetMempalaceInitDone flips the init flag for one project. Called on
// successful `mempalace init` completion; left false on failure so the
// startup sweep retries.
func (q *Queries) SetMempalaceInitDone(ctx context.Context, projectID int32, done bool) error {
	return q.db.WithContext(ctx).Model(&Project{}).Where("id = ?", projectID).Update("mempalace_init_done", done).Error
}
