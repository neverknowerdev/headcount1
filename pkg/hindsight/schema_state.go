package hindsight

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"

	"agent-orchestrator/db"
)

// schemaState records which Postgres schema the memory backend stores data
// in. It only exists after a fallback switch (see Manager.Start): a fresh
// install runs on the default schema with no state file at all. Persisting
// the switch means restarts go straight to the working schema instead of
// re-crashing on the incompatible one first.
type schemaState struct {
	Schema           string `json:"schema"`
	PreviousSchema   string `json:"previous_schema,omitempty"`
	Reason           string `json:"reason,omitempty"`
	HindsightVersion string `json:"hindsight_version,omitempty"`
	SwitchedAt       string `json:"switched_at,omitempty"`
}

func schemaStatePath() string {
	return filepath.Join(db.PaperclipHome(), "data", "hindsight", "schema-state.json")
}

func loadSchemaState() (schemaState, bool) {
	data, err := os.ReadFile(schemaStatePath())
	if err != nil {
		return schemaState{}, false
	}
	var s schemaState
	if err := json.Unmarshal(data, &s); err != nil || s.Schema == "" {
		return schemaState{}, false
	}
	return s, true
}

func saveSchemaState(s schemaState) error {
	path := schemaStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// resolveSchema picks the storage schema for this run:
//  1. HINDSIGHT_API_DATABASE_SCHEMA env override (explicit user intent),
//  2. the persisted schema-state file (a previous fallback switch),
//  3. "public" — hindsight-api's default, so existing healthy installs keep
//     finding their data.
func resolveSchema() string {
	if s := os.Getenv("HINDSIGHT_API_DATABASE_SCHEMA"); s != "" {
		return s
	}
	if st, ok := loadSchemaState(); ok {
		if st.Reason != "" {
			log.Printf("hindsight: using schema %q (switched %s: %s)", st.Schema, st.SwitchedAt, st.Reason)
		}
		return st.Schema
	}
	return "public"
}
