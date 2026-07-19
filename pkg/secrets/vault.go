package secrets

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// vaultKeySource fetches the master key from HashiCorp Vault's KV secrets
// engine over its HTTP API (a minimal client — the official SDK would be a
// heavy dependency for a single authenticated GET).
//
// Configuration (env vars):
//
//	VAULT_ADDR                      https://vault.example.com:8200 (selects this source)
//	VAULT_TOKEN                     auth token
//	VAULT_NAMESPACE                 optional, Vault Enterprise namespaces
//	HEADCOUNT1_VAULT_SECRET_PATH     API path of the secret, default "secret/data/headcount1" (KV v2)
//	HEADCOUNT1_VAULT_SECRET_FIELD    field inside the secret, default "master_key"
//	HEADCOUNT1_VAULT_KEY_TTL_SECONDS in-memory cache TTL for the fetched key, default 300; 0 fetches every time
//
// Unlike the env/file sources, fetches go over the network, so the key is
// cached in memory for the TTL. Revoking the Vault token therefore locks the
// app out of all stored secrets within one TTL at most.
type vaultKeySource struct {
	addr      string
	token     string
	namespace string
	path      string
	field     string
	ttl       time.Duration
	client    *http.Client

	mu        sync.Mutex
	cached    MasterKeyMaterial
	hasCached bool
	fetchedAt time.Time
}

func newVaultKeySourceFromEnv(addr string) *vaultKeySource {
	path := os.Getenv("HEADCOUNT1_VAULT_SECRET_PATH")
	if path == "" {
		path = "secret/data/headcount1"
	}
	field := os.Getenv("HEADCOUNT1_VAULT_SECRET_FIELD")
	if field == "" {
		field = "master_key"
	}
	ttl := 300 * time.Second
	if v := os.Getenv("HEADCOUNT1_VAULT_KEY_TTL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			ttl = time.Duration(n) * time.Second
		}
	}
	return &vaultKeySource{
		addr:      strings.TrimRight(addr, "/"),
		token:     os.Getenv("VAULT_TOKEN"),
		namespace: os.Getenv("VAULT_NAMESPACE"),
		path:      strings.Trim(path, "/"),
		field:     field,
		ttl:       ttl,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (v *vaultKeySource) Name() string {
	return fmt.Sprintf("vault:%s/v1/%s#%s", v.addr, v.path, v.field)
}

func (v *vaultKeySource) MasterKey() (MasterKeyMaterial, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.hasCached && v.ttl > 0 && time.Since(v.fetchedAt) < v.ttl {
		return v.cached, nil
	}
	key, err := v.fetch()
	if err != nil {
		return MasterKeyMaterial{}, err
	}
	v.cached = key
	v.hasCached = true
	v.fetchedAt = time.Now()
	return key, nil
}

func (v *vaultKeySource) fetch() (MasterKeyMaterial, error) {
	var zero MasterKeyMaterial
	if v.token == "" {
		return zero, fmt.Errorf("VAULT_ADDR is set but VAULT_TOKEN is empty")
	}
	req, err := http.NewRequest(http.MethodGet, v.addr+"/v1/"+v.path, nil)
	if err != nil {
		return zero, err
	}
	req.Header.Set("X-Vault-Token", v.token)
	if v.namespace != "" {
		req.Header.Set("X-Vault-Namespace", v.namespace)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return zero, fmt.Errorf("vault request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return zero, fmt.Errorf("vault returned %s for %s: %s", resp.Status, v.path, strings.TrimSpace(string(body)))
	}

	// KV v2 nests the fields under data.data; KV v1 has them under data.
	var parsed struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return zero, fmt.Errorf("vault returned unparseable response: %w", err)
	}
	fields := parsed.Data
	if inner, ok := parsed.Data["data"]; ok {
		var innerFields map[string]json.RawMessage
		if err := json.Unmarshal(inner, &innerFields); err == nil && innerFields != nil {
			fields = innerFields
		}
	}
	raw, ok := fields[v.field]
	if !ok {
		return zero, fmt.Errorf("vault secret %s has no field %q", v.path, v.field)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return zero, fmt.Errorf("vault secret %s field %q is not a string", v.path, v.field)
	}
	return parseMasterKey(value)
}
