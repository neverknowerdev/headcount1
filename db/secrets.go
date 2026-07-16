package db

import (
	"context"
	"fmt"
	"log"

	"agent-orchestrator/pkg/secrets"
	"gorm.io/gorm/schema"
)

// Registering here (rather than in main) guarantees every gorm.Open in the
// codebase — including tests — can parse models tagged `serializer:secret`.
func init() {
	secrets.SetBaseDirResolver(Headcount1Home)
	schema.RegisterSerializer("secret", secrets.GormSerializer{})
}

// SecretsBackend names the active master-key source for startup logging.
func SecretsBackend() string {
	return secrets.Default().SourceName()
}

// EncryptPlaintextSecrets seals any secrets that were stored raw before
// encryption at rest was introduced (LLM provider API keys, MCP account
// tokens). Loading a row decrypts nothing for legacy values (plaintext
// passthrough) and saving it re-writes every secret field through the
// serializer, so this is an idempotent one-way upgrade. Safe to run on every
// startup; a no-op once everything is sealed.
func (q *Queries) EncryptPlaintextSecrets(ctx context.Context) error {
	sealedPattern := secrets.Prefix + "%"

	var providerIDs []int32
	if err := q.db.WithContext(ctx).Raw(
		"SELECT id FROM llm_providers WHERE api_key != '' AND api_key NOT LIKE ?", sealedPattern,
	).Scan(&providerIDs).Error; err != nil {
		return err
	}
	for _, id := range providerIDs {
		var p LLMProvider
		if err := q.db.WithContext(ctx).First(&p, id).Error; err != nil {
			return fmt.Errorf("load llm provider %d: %w", id, err)
		}
		if err := q.db.WithContext(ctx).Save(&p).Error; err != nil {
			return fmt.Errorf("encrypt llm provider %d api key: %w", id, err)
		}
	}

	var accountIDs []int32
	if err := q.db.WithContext(ctx).Raw(
		"SELECT id FROM mcp_accounts WHERE auth_token != '' AND auth_token NOT LIKE ?", sealedPattern,
	).Scan(&accountIDs).Error; err != nil {
		return err
	}
	for _, id := range accountIDs {
		var a MCPAccount
		if err := q.db.WithContext(ctx).First(&a, id).Error; err != nil {
			return fmt.Errorf("load mcp account %d: %w", id, err)
		}
		if err := q.db.WithContext(ctx).Save(&a).Error; err != nil {
			return fmt.Errorf("encrypt mcp account %d token: %w", id, err)
		}
	}

	if n := len(providerIDs) + len(accountIDs); n > 0 {
		log.Printf("secrets: encrypted %d previously plaintext secret(s) at rest", n)
	}
	return nil
}
