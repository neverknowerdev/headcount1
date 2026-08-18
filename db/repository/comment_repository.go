package repository

import (
	. "agent-orchestrator/db/models"
	"context"
	"errors"
	"fmt"
	"gorm.io/gorm"
)

type CommentRepository struct{ db *gorm.DB }

func NewCommentRepository(db *gorm.DB) *CommentRepository {
	return &CommentRepository{db: db}
}
func (q *CommentRepository) CreateComment(ctx context.Context, c Comment) (Comment, error) {
	err := q.db.WithContext(ctx).Create(&c).Error
	return c, err
}
func (q *CommentRepository) ListCommentsByTask(ctx context.Context, taskID int32) ([]Comment, error) {
	var c []Comment
	err := q.db.WithContext(ctx).Where("task_id = ?", taskID).Order("created_at asc").Find(&c).Error
	return c, err
}

// FindPendingHumanQuestion returns the oldest unanswered ask_user comment.
// Comments are the durable conversation record, so a restart does not lose
// the wait. A later human comment resolves the currently pending question;
// unrelated comments before the next question do not.
func (q *CommentRepository) FindPendingHumanQuestion(ctx context.Context, taskID int32) (Comment, bool, error) {
	comments, err := q.ListCommentsByTask(ctx, taskID)
	if err != nil {
		return Comment{}, false, err
	}
	var pending Comment
	for _, comment := range comments {
		switch {
		case comment.CommentType == "ask_user" && comment.AuthorType == "agent":
			if pending.ID == 0 {
				pending = comment
			}
		case pending.ID != 0 && comment.AuthorType == "human" && comment.ID > pending.ID:
			pending = Comment{}
		}
	}
	return pending, pending.ID != 0, nil
}
func (q *CommentRepository) ListAllComments(ctx context.Context) ([]Comment, error) {
	var comments []Comment
	err := q.db.WithContext(ctx).Order("id").Find(&comments).Error
	return comments, err
}
func (q *CommentRepository) CreateGitHubWebhookComments(ctx context.Context, deliveryID string, repositoryID int64, pullRequestNumber int, content string) ([]Comment, error) {
	comments := []Comment{}
	err := q.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var projects []Project
		if err := tx.Where("git_hub_repository_id = ?", repositoryID).Find(&projects).Error; err != nil {
			return fmt.Errorf("load projects for GitHub webhook: %w", err)
		}
		for _, project := range projects {
			var task Task
			err := tx.Where("project_id = ? AND git_hub_pr_number = ?", project.ID, pullRequestNumber).Order("id desc").First(&task).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			if err != nil {
				return fmt.Errorf("load task for GitHub webhook: %w", err)
			}
			var existing GitHubWebhookTarget
			err = tx.Where("delivery_id = ? AND task_id = ?", deliveryID, task.ID).First(&existing).Error
			if err == nil {
				continue
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("load GitHub webhook target: %w", err)
			}
			comment := Comment{TaskID: task.ID, AuthorType: "github", CommentType: "github", Content: content}
			if err := tx.Create(&comment).Error; err != nil {
				return fmt.Errorf("save GitHub comment: %w", err)
			}
			if err := tx.Create(&GitHubWebhookTarget{DeliveryID: deliveryID, TaskID: task.ID, CommentID: comment.ID}).Error; err != nil {
				return fmt.Errorf("save GitHub webhook target: %w", err)
			}
			comments = append(comments, comment)
		}
		return nil
	})
	return comments, err
}
