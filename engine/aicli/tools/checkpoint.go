package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"

	"agent-orchestrator/engine/aicli"
)

// RecordInput is one decision, dead end or assumption as an executor states
// it. The same three fields serve all three kinds: what, and why.
type RecordInput struct {
	Title string `json:"title"`
	// Detail is what was decided, what was tried, or what is assumed.
	Detail string `json:"detail"`
	// Reason is why it was decided, why it failed, or why it is reasonable.
	Reason       string   `json:"reason"`
	Alternatives []string `json:"alternatives,omitempty"`
	// Revises is the ID of an earlier record this one replaces.
	Revises int64 `json:"revises,omitempty"`
}

func validateRecords(field string, records []RecordInput) error {
	for i, record := range records {
		if strings.TrimSpace(record.Title) == "" {
			return fmt.Errorf("%s[%d].title is empty", field, i)
		}
		if strings.TrimSpace(record.Detail) == "" {
			return fmt.Errorf("%s[%d].detail is empty", field, i)
		}
	}
	return nil
}

const recordItemSchema = `{"type":"object","properties":{
"title":{"type":"string","description":"A few words naming it."},
"detail":{"type":"string","description":"%s"},
"reason":{"type":"string","description":"%s"},
"alternatives":{"type":"array","items":{"type":"string"},"description":"Other options considered."},
"revises":{"type":"integer","description":"ID of an earlier record this one replaces, if you changed your mind."}},
"required":["title","detail","reason"]}`

func recordListSchema(description, detail, reason string) string {
	encoded, _ := json.Marshal(description)
	return fmt.Sprintf(`{"type":"array","description":%s,"items":%s}`, encoded, fmt.Sprintf(recordItemSchema, detail, reason))
}

// CheckpointInput is one checkpoint of an executor's work.
type CheckpointInput struct {
	Progress    string        `json:"progress"`
	NextStep    string        `json:"next_step"`
	Decisions   []RecordInput `json:"decisions,omitempty"`
	DeadEnds    []RecordInput `json:"dead_ends,omitempty"`
	Assumptions []RecordInput `json:"assumptions,omitempty"`
}

// Checkpoint is the tool an executor uses to say where it stands and to
// record what it has decided, tried and assumed since it last did. The host
// may make it the only tool of a turn, so that the record is kept even when
// the model would not volunteer it.
type Checkpoint struct {
	fn func(context.Context, CheckpointInput) (string, error)
	// progressOnly drops the record fields from the schema for a turn in
	// which the host already knows there is nothing new to record.
	progressOnly atomic.Bool
}

func NewCheckpoint(fn func(context.Context, CheckpointInput) (string, error)) *Checkpoint {
	return &Checkpoint{fn: fn}
}

// SetProgressOnly narrows or restores the tool's schema.
func (t *Checkpoint) SetProgressOnly(only bool) { t.progressOnly.Store(only) }

func (t *Checkpoint) Def() aicli.ToolDef {
	properties := `"progress":{"type":"string","description":"What has been done so far, in a few sentences."},
"next_step":{"type":"string","description":"What you will do next."}`
	description := "Record where the work stands. Call it when you commit to an approach, abandon one, or assume something you have not verified; you will also be asked to at intervals."
	if !t.progressOnly.Load() {
		description += " List only what is new since your last checkpoint: empty lists are correct when nothing new was decided. Do not repeat earlier records; to change one, name it in `revises`."
		properties += `,
"decisions":` + recordListSchema("Choices you made since the last checkpoint.", "What you decided.", "Why.") + `,
"dead_ends":` + recordListSchema("Approaches you tried since the last checkpoint and abandoned.", "What you tried.", "Why it did not work.") + `,
"assumptions":` + recordListSchema("Things you are treating as true without having verified them.", "What you assume.", "Why it is reasonable.")
	}
	return aicli.ToolDef{Type: "function", Function: aicli.FuncMeta{
		Name:        string(aicli.ToolCheckpoint),
		Description: description,
		Parameters:  json.RawMessage(`{"type":"object","properties":{` + properties + `},"required":["progress","next_step"]}`),
	}}
}

func (t *Checkpoint) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input CheckpointInput
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("checkpoint: %w", err)
	}
	if strings.TrimSpace(input.Progress) == "" {
		return "", fmt.Errorf("checkpoint: progress is required")
	}
	for field, records := range map[string][]RecordInput{"decisions": input.Decisions, "dead_ends": input.DeadEnds, "assumptions": input.Assumptions} {
		if err := validateRecords(field, records); err != nil {
			return "", fmt.Errorf("checkpoint: %w", err)
		}
	}
	if t.progressOnly.Load() {
		// The records were not asked for in this turn; ignore any sent anyway.
		input.Decisions, input.DeadEnds, input.Assumptions = nil, nil, nil
	}
	return t.fn(ctx, input)
}
