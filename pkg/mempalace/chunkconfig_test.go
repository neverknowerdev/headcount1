package mempalace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"agent-orchestrator/db"
)

func TestEnsureChunkConfigWritesAndMerges(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := ensureChunkConfig(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".mempalace", "config.json")
	read := func() map[string]any {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if err := json.Unmarshal(data, &cfg); err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	cfg := read()
	if intValue(cfg["chunk_size"]) != chunkSize || intValue(cfg["chunk_overlap"]) != chunkOverlap || intValue(cfg["min_chunk_size"]) != minChunkSize {
		t.Errorf("chunk config not written: %v", cfg)
	}

	// Foreign keys survive a re-write, and stale chunk values are corrected.
	cfg["embedding_model"] = "minilm"
	cfg["chunk_size"] = 800
	data, _ := json.Marshal(cfg)
	os.WriteFile(path, data, 0600)

	if err := ensureChunkConfig(); err != nil {
		t.Fatal(err)
	}
	cfg = read()
	if intValue(cfg["chunk_size"]) != chunkSize {
		t.Errorf("stale chunk_size not corrected: %v", cfg["chunk_size"])
	}
	if cfg["embedding_model"] != "minilm" {
		t.Error("unrelated config keys must be preserved")
	}
}

func TestWakeUpTTLShortensWhileRunsActive(t *testing.T) {
	company := db.Company{ShortName: "ttl-test-co"}
	palace := PalacePath(company)

	if got := wakeUpTTL(palace); got != idleWakeUpTTL {
		t.Errorf("idle TTL = %v, want %v", got, idleWakeUpTTL)
	}
	RunStarted(company)
	RunStarted(company)
	if got := wakeUpTTL(palace); got != activeWakeUpTTL {
		t.Errorf("active TTL = %v, want %v", got, activeWakeUpTTL)
	}
	RunEnded(company)
	if got := wakeUpTTL(palace); got != activeWakeUpTTL {
		t.Errorf("TTL with one remaining run = %v, want %v", got, activeWakeUpTTL)
	}
	RunEnded(company)
	if got := wakeUpTTL(palace); got != idleWakeUpTTL {
		t.Errorf("TTL after all runs ended = %v, want %v", got, idleWakeUpTTL)
	}
	// Unbalanced RunEnded must not underflow.
	RunEnded(company)
	if got := wakeUpTTL(palace); got != idleWakeUpTTL {
		t.Errorf("TTL after extra RunEnded = %v", got)
	}
}
