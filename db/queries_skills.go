package db

import "context"

func (q *Queries) CreateSkill(ctx context.Context, s Skill) (Skill, error) {
	err := q.db.WithContext(ctx).Create(&s).Error
	return s, err
}
