package db

import (
	"context"
	"fmt"
	"time"

	"agent-orchestrator/pkg/secrets"
)

// CreateEnvironmentConnector links an environment to a deploy target. The API
// token is sealed here under the calling user's DEK — returns
// secrets.ErrLocked when their vault is locked.
func (q *Queries) CreateEnvironmentConnector(ctx context.Context, c EnvironmentConnector, userID int32, token string) (EnvironmentConnector, error) {
	switch c.Provider {
	case ConnectorVercel, ConnectorGitHub:
	default:
		return EnvironmentConnector{}, fmt.Errorf("invalid provider %q: must be %q or %q", c.Provider, ConnectorVercel, ConnectorGitHub)
	}
	if c.TargetProject == "" || c.TargetEnvironment == "" {
		return EnvironmentConnector{}, fmt.Errorf("target project and target environment are required")
	}
	if token == "" {
		return EnvironmentConnector{}, fmt.Errorf("api token is required")
	}
	sealed, err := secrets.Default().EncryptForUser(userID, token)
	if err != nil {
		return EnvironmentConnector{}, err
	}
	c.ApiTokenEncrypted = sealed
	c.UserID = &userID
	err = q.db.WithContext(ctx).Create(&c).Error
	c.HasToken = c.ApiTokenEncrypted != ""
	return c, err
}

// GetEnvironmentConnector returns one connector by id.
func (q *Queries) GetEnvironmentConnector(ctx context.Context, id int32) (EnvironmentConnector, error) {
	var c EnvironmentConnector
	err := q.db.WithContext(ctx).First(&c, id).Error
	c.HasToken = c.ApiTokenEncrypted != ""
	return c, err
}

// ListEnvironmentConnectors returns an environment's connectors.
func (q *Queries) ListEnvironmentConnectors(ctx context.Context, environmentID int32) ([]EnvironmentConnector, error) {
	var cs []EnvironmentConnector
	err := q.db.WithContext(ctx).
		Where("environment_id = ?", environmentID).
		Order("id asc").
		Find(&cs).Error
	for i := range cs {
		cs[i].HasToken = cs[i].ApiTokenEncrypted != ""
	}
	return cs, err
}

// DeleteEnvironmentConnector removes a connector. The remote target keeps
// whatever was already pushed — disconnecting never destroys deploy config.
func (q *Queries) DeleteEnvironmentConnector(ctx context.Context, id int32) error {
	return q.db.WithContext(ctx).Delete(&EnvironmentConnector{}, id).Error
}

// SetConnectorSyncResult records the outcome of a push so the UI can show
// per-connector sync state.
func (q *Queries) SetConnectorSyncResult(ctx context.Context, id int32, syncErr error) error {
	now := time.Now().UTC()
	fields := map[string]interface{}{
		"last_sync_at":     &now,
		"last_sync_status": "ok",
		"last_sync_error":  "",
	}
	if syncErr != nil {
		fields["last_sync_status"] = "error"
		fields["last_sync_error"] = syncErr.Error()
	}
	return q.db.WithContext(ctx).Model(&EnvironmentConnector{}).Where("id = ?", id).Updates(fields).Error
}
