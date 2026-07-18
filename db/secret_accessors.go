package db

import "agent-orchestrator/pkg/secrets"

// The models store secrets as ciphertext (the *Encrypted fields). These
// accessors are the ONLY place a stored secret is turned back into plaintext.
// Call them at the exact point of use and use the result immediately — never
// stash the returned plaintext on a long-lived struct. They return
// secrets.ErrLocked when the owning user's vault is locked.

// DecryptAPIKey returns the provider's API key in plaintext.
func (p LLMProvider) DecryptAPIKey() (string, error) {
	return secrets.Default().Open(p.ApiKeyEncrypted)
}

// DecryptAuthToken returns the MCP account's auth token (or credentials-file
// path) in plaintext.
func (a MCPAccount) DecryptAuthToken() (string, error) {
	return secrets.Default().Open(a.AuthTokenEncrypted)
}

// DecryptSSHKey returns the user's git SSH private key in plaintext.
func (c UserGitCredential) DecryptSSHKey() (string, error) {
	return secrets.Default().Open(c.SSHPrivateKeyEncrypted)
}
