package mempalace

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Chunking configuration for palace writes. MemPalace's default chunk_size of
// 800 chars hard-cuts typical agent memories (600–1,200 chars) mid-word;
// these values keep a stored fact intact in one chunk while still bounding
// drawer size. They are read by the mempalace package (config.py:
// MempalaceConfig) from ~/.mempalace/config.json — chunk keys have no env-var
// override, so the config file is the only wiring mechanism. Config affects
// new writes only; existing palace data is not re-chunked.
const (
	chunkSize    = 2400
	chunkOverlap = 200
	minChunkSize = 100
)

// ensureChunkConfig merges the orchestrator's chunking settings into the
// mempalace user config file (~/.mempalace/config.json), preserving any other
// keys. Called from palace provisioning; safe to call repeatedly.
func ensureChunkConfig() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home dir: %w", err)
	}
	dir := filepath.Join(home, ".mempalace")
	path := filepath.Join(dir, "config.json")

	cfg := map[string]any{}
	if data, rErr := os.ReadFile(path); rErr == nil {
		// A corrupt config is replaced rather than propagated — mempalace
		// itself ignores unparseable config files.
		_ = json.Unmarshal(data, &cfg)
	}

	if intValue(cfg["chunk_size"]) == chunkSize &&
		intValue(cfg["chunk_overlap"]) == chunkOverlap &&
		intValue(cfg["min_chunk_size"]) == minChunkSize {
		return nil
	}
	cfg["chunk_size"] = chunkSize
	cfg["chunk_overlap"] = chunkOverlap
	cfg["min_chunk_size"] = minChunkSize

	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create mempalace config dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write mempalace config: %w", err)
	}
	return os.Rename(tmp, path)
}

// intValue coerces the numeric shapes json.Unmarshal can produce.
func intValue(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}
