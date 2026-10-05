package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"gorm.io/gorm"
)

type SkillRepository struct{ db *gorm.DB }

func NewSkillRepository(db *gorm.DB) *SkillRepository { return &SkillRepository{db: db} }
func (q *SkillRepository) CreateSkill(ctx context.Context, s Skill) (Skill, error) {
	err := q.db.WithContext(ctx).Create(&s).Error
	return s, err
}

func (q *SkillRepository) ListAllSkills(ctx context.Context) ([]Skill, error) {
	var skills []Skill
	err := q.db.WithContext(ctx).Order("id").Find(&skills).Error
	return skills, err
}
