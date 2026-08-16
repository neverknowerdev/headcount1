package server

import (
	"agent-orchestrator/db/migrations"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"agent-orchestrator/db"

	"github.com/glebarez/sqlite"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type webhookEngine struct {
	mu       sync.Mutex
	errors   []error
	runCalls int
}

func (e *webhookEngine) ProcessTask(context.Context, int32) error { return nil }
func (e *webhookEngine) StopRun(context.Context, int32)           {}
func (e *webhookEngine) RerunTask(context.Context, int32) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.runCalls++
	if len(e.errors) == 0 {
		return nil
	}
	err := e.errors[0]
	e.errors = e.errors[1:]
	return err
}

func webhookSignature(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestGitHubWebhookIsPublicAndDeliveryIsIdempotent(t *testing.T) {
	t.Setenv("HEADCOUNT1_GITHUB_WEBHOOK_FORWARD_SECRET", "relay-secret")
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))

	router := chi.NewRouter()
	NewServer(database, nil).MountPublic(router)
	body := []byte(`{"repository":{"id":42}}`)
	makeRequest := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", "push")
		req.Header.Set("X-GitHub-Delivery", "delivery-1")
		req.Header.Set("X-Headcount1-Webhook-Forward-Signature", webhookSignature(body, "relay-secret"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	require.Equal(t, http.StatusNoContent, makeRequest().Code)
	require.Equal(t, http.StatusNoContent, makeRequest().Code)
	var deliveries int64
	require.NoError(t, database.Model(&db.GitHubWebhookDelivery{}).Count(&deliveries).Error)
	require.Equal(t, int64(1), deliveries)
}

func TestGitHubWebhookCommitsEachDeliveryTargetOnce(t *testing.T) {
	t.Setenv("HEADCOUNT1_GITHUB_WEBHOOK_FORWARD_SECRET", "relay-secret")
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	project := db.Project{GitHubRepositoryID: 42}
	require.NoError(t, database.Create(&project).Error)
	task := db.Task{ProjectID: &project.ID, GitHubPRNumber: 7}
	require.NoError(t, database.Create(&task).Error)

	router := chi.NewRouter()
	NewServer(database, nil).MountPublic(router)
	body := []byte(`{"action":"created","repository":{"id":42},"issue":{"number":7,"pull_request":{}},"comment":{"body":"please fix","user":{"login":"octocat"}}}`)
	makeRequest := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", "issue_comment")
		req.Header.Set("X-GitHub-Delivery", "delivery-comment-1")
		req.Header.Set("X-Headcount1-Webhook-Forward-Signature", webhookSignature(body, "relay-secret"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	require.Equal(t, http.StatusNoContent, makeRequest().Code)
	require.Equal(t, http.StatusNoContent, makeRequest().Code)
	var comments, targets int64
	require.NoError(t, database.Model(&db.Comment{}).Count(&comments).Error)
	require.NoError(t, database.Model(&db.GitHubWebhookTarget{}).Count(&targets).Error)
	require.Equal(t, int64(1), comments)
	require.Equal(t, int64(1), targets)
	var delivery db.GitHubWebhookDelivery
	require.NoError(t, database.Where("delivery_id = ?", "delivery-comment-1").First(&delivery).Error)
	require.Equal(t, "completed", delivery.Status)
}

func TestGitHubWebhookRetriesWakeWithoutDuplicatingComment(t *testing.T) {
	t.Setenv("HEADCOUNT1_GITHUB_WEBHOOK_FORWARD_SECRET", "relay-secret")
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	project := db.Project{GitHubRepositoryID: 42}
	require.NoError(t, database.Create(&project).Error)
	task := db.Task{ProjectID: &project.ID, GitHubPRNumber: 7}
	require.NoError(t, database.Create(&task).Error)
	eng := &webhookEngine{errors: []error{errors.New("engine unavailable"), nil}}
	router := chi.NewRouter()
	NewServer(database, eng).MountPublic(router)
	body := []byte(`{"action":"created","repository":{"id":42},"issue":{"number":7,"pull_request":{}},"comment":{"body":"please fix","user":{"login":"octocat"}}}`)
	request := func() int {
		req := httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", "issue_comment")
		req.Header.Set("X-GitHub-Delivery", "retry-wake-1")
		req.Header.Set("X-Headcount1-Webhook-Forward-Signature", webhookSignature(body, "relay-secret"))
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w.Code
	}

	require.Equal(t, http.StatusInternalServerError, request())
	var comments int64
	require.NoError(t, database.Model(&db.Comment{}).Count(&comments).Error)
	require.Equal(t, int64(1), comments)
	var target db.GitHubWebhookTarget
	require.NoError(t, database.Where("delivery_id = ?", "retry-wake-1").First(&target).Error)
	require.Equal(t, "pending", target.WakeStatus)
	require.Equal(t, http.StatusNoContent, request())
	require.NoError(t, database.Model(&db.Comment{}).Count(&comments).Error)
	require.Equal(t, int64(1), comments)
	require.NoError(t, database.Where("delivery_id = ?", "retry-wake-1").First(&target).Error)
	require.Equal(t, "completed", target.WakeStatus)
	var delivery db.GitHubWebhookDelivery
	require.NoError(t, database.Where("delivery_id = ?", "retry-wake-1").First(&delivery).Error)
	require.Equal(t, "completed", delivery.Status)
	require.Equal(t, 2, eng.runCalls)
}

func TestGitHubWebhookIgnoresEditedCommentAndReclaimsExpiredLease(t *testing.T) {
	t.Setenv("HEADCOUNT1_GITHUB_WEBHOOK_FORWARD_SECRET", "relay-secret")
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	past := time.Now().Add(-time.Minute)
	require.NoError(t, database.Create(&db.GitHubWebhookDelivery{DeliveryID: "stale-lease-1", Event: "issue_comment", Status: "processing", AttemptToken: "old", LeaseExpiresAt: &past}).Error)
	router := chi.NewRouter()
	NewServer(database, nil).MountPublic(router)
	body := []byte(`{"action":"edited","repository":{"id":42},"issue":{"number":7,"pull_request":{}},"comment":{"body":"edited","user":{"login":"octocat"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-GitHub-Delivery", "stale-lease-1")
	req.Header.Set("X-Headcount1-Webhook-Forward-Signature", webhookSignature(body, "relay-secret"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusNoContent, w.Code)
	var delivery db.GitHubWebhookDelivery
	require.NoError(t, database.Where("delivery_id = ?", "stale-lease-1").First(&delivery).Error)
	require.Equal(t, "completed", delivery.Status)
	require.NotEqual(t, "old", delivery.AttemptToken)
	var comments int64
	require.NoError(t, database.Model(&db.Comment{}).Count(&comments).Error)
	require.Zero(t, comments)
}

func TestGitHubWebhookRejectsMissingDeliveryBeforeSideEffects(t *testing.T) {
	t.Setenv("HEADCOUNT1_GITHUB_WEBHOOK_FORWARD_SECRET", "relay-secret")
	database, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, migrations.ApplyGORM(database, "sqlite", "test"))
	project := db.Project{GitHubRepositoryID: 42}
	require.NoError(t, database.Create(&project).Error)
	task := db.Task{ProjectID: &project.ID, GitHubPRNumber: 7}
	require.NoError(t, database.Create(&task).Error)
	router := chi.NewRouter()
	NewServer(database, &webhookEngine{}).MountPublic(router)
	body := []byte(`{"action":"created","repository":{"id":42},"issue":{"number":7,"pull_request":{}},"comment":{"body":"please fix","user":{"login":"octocat"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/github/webhook", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-Headcount1-Webhook-Forward-Signature", webhookSignature(body, "relay-secret"))
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	var comments, deliveries int64
	require.NoError(t, database.Model(&db.Comment{}).Count(&comments).Error)
	require.NoError(t, database.Model(&db.GitHubWebhookDelivery{}).Count(&deliveries).Error)
	require.Zero(t, comments)
	require.Zero(t, deliveries)
}
