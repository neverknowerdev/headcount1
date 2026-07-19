package secrets

import (
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testStore(t *testing.T) (*SecretManager, string) {
	t.Helper()
	dir := t.TempDir()
	src := &fileKeySource{path: filepath.Join(dir, "master.key")}
	return NewManager(src, filepath.Join(dir, "keystore.json")), dir
}

func TestSealOpenRoundtrip(t *testing.T) {
	s, _ := testStore(t)
	sealed, err := s.Encrypt("sk-super-secret")
	if err != nil {
		t.Fatal(err)
	}
	if !IsSealed(sealed) {
		t.Fatalf("sealed value missing prefix: %q", sealed)
	}
	if strings.Contains(sealed, "super-secret") {
		t.Fatal("plaintext leaked into sealed value")
	}
	plain, err := s.Decrypt(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "sk-super-secret" {
		t.Fatalf("roundtrip mismatch: %q", plain)
	}
}

func TestSealEmptyAndDoubleSeal(t *testing.T) {
	s, _ := testStore(t)
	if v, err := s.Encrypt(""); err != nil || v != "" {
		t.Fatalf("empty must stay empty, got %q, %v", v, err)
	}
	sealed, _ := s.Encrypt("value")
	again, err := s.Encrypt(sealed)
	if err != nil || again != sealed {
		t.Fatalf("sealing a sealed value must be a no-op, got %q, %v", again, err)
	}
}

func TestOpenLegacyPlaintextPassthrough(t *testing.T) {
	s, _ := testStore(t)
	plain, err := s.Decrypt("legacy-plaintext-key")
	if err != nil || plain != "legacy-plaintext-key" {
		t.Fatalf("legacy passthrough failed: %q, %v", plain, err)
	}
}

func TestKeystorePersistsAcrossStores(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "master.key")
	ksPath := filepath.Join(dir, "keystore.json")

	s1 := NewManager(&fileKeySource{path: keyPath}, ksPath)
	sealed, err := s1.Encrypt("persist-me")
	if err != nil {
		t.Fatal(err)
	}

	// A fresh store (fresh process) with the same key + keystore must decrypt.
	s2 := NewManager(&fileKeySource{path: keyPath}, ksPath)
	plain, err := s2.Decrypt(sealed)
	if err != nil || plain != "persist-me" {
		t.Fatalf("cross-store open failed: %q, %v", plain, err)
	}
}

func TestWrongMasterKeyFailsClearly(t *testing.T) {
	dir := t.TempDir()
	ksPath := filepath.Join(dir, "keystore.json")

	s1 := NewManager(&fileKeySource{path: filepath.Join(dir, "a.key")}, ksPath)
	sealed, err := s1.Encrypt("secret")
	if err != nil {
		t.Fatal(err)
	}

	s2 := NewManager(&fileKeySource{path: filepath.Join(dir, "b.key")}, ksPath)
	if _, err := s2.Decrypt(sealed); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected fingerprint-mismatch error, got %v", err)
	}
}

func TestFileKeySourceGeneratesPrivateKeyFile(t *testing.T) {
	dir := t.TempDir()
	src := &fileKeySource{path: filepath.Join(dir, "master.key")}
	k1, err := src.MasterKey()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(src.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("key file mode = %v, want 0600", info.Mode().Perm())
	}
	k2, err := src.MasterKey()
	if err != nil || k1.Key != k2.Key {
		t.Fatalf("key not stable across reads: %v", err)
	}
}

func TestParseMasterKeyFormats(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i)
	}
	fromHex, err := parseMasterKey(hex.EncodeToString(raw))
	if err != nil {
		t.Fatal(err)
	}
	if fromHex.Passphrase || fromHex.Key[0] != 0 || fromHex.Key[31] != 31 {
		t.Fatal("hex key not decoded verbatim as a high-entropy key")
	}
	// A passphrase is carried raw (Argon2id happens later, over the keystore
	// salt) — it must be flagged Passphrase and preserved verbatim.
	pass, _ := parseMasterKey("correct horse battery staple")
	if !pass.Passphrase || string(pass.Raw) != "correct horse battery staple" {
		t.Fatal("passphrase not carried as raw stretch material")
	}
	if _, err := parseMasterKey("  "); err == nil {
		t.Fatal("empty key must error")
	}
}

func TestEnvKeySource(t *testing.T) {
	t.Setenv(EnvMasterKey, "some-passphrase")
	s := NewManager(envKeySource{}, filepath.Join(t.TempDir(), "keystore.json"))
	sealed, err := s.Encrypt("v")
	if err != nil {
		t.Fatal(err)
	}
	if plain, err := s.Decrypt(sealed); err != nil || plain != "v" {
		t.Fatalf("env source roundtrip failed: %q, %v", plain, err)
	}
}

func TestVaultKeySourceKV2AndTTLCache(t *testing.T) {
	var fetches int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&fetches, 1)
		if r.Header.Get("X-Vault-Token") != "test-token" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Path != "/v1/secret/data/headcount1" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// KV v2 shape: data.data.<field>
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"data": map[string]any{"master_key": "vault-passphrase"}},
		})
	}))
	defer srv.Close()

	src := &vaultKeySource{
		addr: srv.URL, token: "test-token",
		path: "secret/data/headcount1", field: "master_key",
		ttl: time.Hour, client: srv.Client(),
	}
	k1, err := src.MasterKey()
	if err != nil {
		t.Fatal(err)
	}
	k2, err := src.MasterKey()
	if err != nil || k1.Passphrase != k2.Passphrase || string(k1.Raw) != string(k2.Raw) || k1.Key != k2.Key {
		t.Fatalf("cached fetch mismatch: %v", err)
	}
	if n := atomic.LoadInt64(&fetches); n != 1 {
		t.Fatalf("expected 1 fetch within TTL, got %d", n)
	}

	// Expired TTL refetches.
	src.fetchedAt = time.Now().Add(-2 * time.Hour)
	if _, err := src.MasterKey(); err != nil {
		t.Fatal(err)
	}
	if n := atomic.LoadInt64(&fetches); n != 2 {
		t.Fatalf("expected refetch after TTL, got %d fetches", n)
	}
}

func TestVaultKeySourceKV1(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// KV v1 shape: data.<field>
		json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{"master_key": "vault-passphrase"},
		})
	}))
	defer srv.Close()
	src := &vaultKeySource{
		addr: srv.URL, token: "t",
		path: "kv/headcount1", field: "master_key",
		ttl: 0, client: srv.Client(),
	}
	if _, err := src.MasterKey(); err != nil {
		t.Fatal(err)
	}
}

func TestVaultErrorsSurface(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"errors":["permission denied"]}`, http.StatusForbidden)
	}))
	defer srv.Close()
	src := &vaultKeySource{addr: srv.URL, token: "t", path: "secret/data/x", field: "master_key", client: srv.Client()}
	s := NewManager(src, filepath.Join(t.TempDir(), "keystore.json"))
	if _, err := s.Encrypt("v"); err == nil || !strings.Contains(err.Error(), "vault returned") {
		t.Fatalf("expected vault error to surface, got %v", err)
	}
}
