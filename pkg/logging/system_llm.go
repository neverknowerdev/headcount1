package logging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SystemLLMFile is the on-disk record of one non-session LLM call — the full
// request and response bodies that are too bulky for the DB row. Written to
// {basePath}/data/logs/system-llm/YYYY-MM-DD/<unixms>-<source>.json so daily
// folders stay browsable and old days are easy to prune.
type SystemLLMFile struct {
	Time     string          `json:"time"`
	Source   string          `json:"source"`
	Detail   string          `json:"detail,omitempty"`
	Provider string          `json:"provider,omitempty"`
	Model    string          `json:"model,omitempty"`
	Request  json.RawMessage `json:"request,omitempty"`
	Response json.RawMessage `json:"response,omitempty"`
	Error    string          `json:"error,omitempty"`
}

// WriteSystemLLMFile persists one system LLM exchange and returns the file
// path (for the DB row's LogFilePath). Request/response are stored verbatim
// when they are valid JSON, else wrapped as JSON strings.
func WriteSystemLLMFile(basePath string, f SystemLLMFile) (string, error) {
	now := time.Now().UTC()
	if f.Time == "" {
		f.Time = now.Format(time.RFC3339)
	}
	dir := filepath.Join(basePath, "data", "logs", "system-llm", now.Format("2006-01-02"))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	f.Request = ensureRawJSON(f.Request)
	f.Response = ensureRawJSON(f.Response)
	name := fmt.Sprintf("%d-%s.json", now.UnixMilli(), sanitizeSource(f.Source))
	path := filepath.Join(dir, name)
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	return path, nil
}

// ensureRawJSON keeps valid JSON as-is and encodes anything else as a JSON
// string so the output file always parses.
func ensureRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	if json.Valid(raw) {
		return raw
	}
	quoted, _ := json.Marshal(string(raw))
	return quoted
}

func sanitizeSource(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+32)
		default:
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		return "unknown"
	}
	return string(out)
}
