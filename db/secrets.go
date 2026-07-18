package db

import (
	"agent-orchestrator/pkg/secrets"
	"gorm.io/gorm/schema"
)

// Registering here (rather than in main) guarantees every gorm.Open in the
// codebase — including tests — can parse models tagged `serializer:sealed`.
// The serializer stores/loads sealed secret columns verbatim (ciphertext in the
// struct); the app seals on write and decrypts at the point of use.
func init() {
	secrets.SetBaseDirResolver(Headcount1Home)
	schema.RegisterSerializer("sealed", secrets.SealedColumnSerializer{})
}

// SecretsBackend names the active master-key source for startup logging.
func SecretsBackend() string {
	return secrets.Default().SourceName()
}
