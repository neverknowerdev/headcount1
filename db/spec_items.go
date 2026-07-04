package db

import "encoding/json"

// Spec item verification statuses. Items start as pending and must be marked
// passed or failed (via the verify_spec_items tool) before a task can finish.
const (
	SpecItemPending = "pending"
	SpecItemPassed  = "passed"
	SpecItemFailed  = "failed"
)

// MaxSpecItems caps how many acceptance criteria / test cases a task can
// carry. Nobody reads big texts.
const MaxSpecItems = 10

// SpecItem is one acceptance criterion or test case. Stored as a JSON array
// in Task.AcceptanceCriteria / Task.TestCases so agents and the UI can track
// each item individually instead of a wall of text.
type SpecItem struct {
	ID     int    `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
}

// NewSpecItems builds a pending item list from plain texts, assigning
// sequential 1-based ids.
func NewSpecItems(texts []string) []SpecItem {
	items := make([]SpecItem, 0, len(texts))
	for i, text := range texts {
		items = append(items, SpecItem{ID: i + 1, Text: text, Status: SpecItemPending})
	}
	return items
}

// ParseSpecItems decodes a stored item list. Returns nil for empty input or
// legacy plain-text content (pre-structured tasks), which callers should
// treat as "no structured items".
func ParseSpecItems(raw string) []SpecItem {
	if raw == "" {
		return nil
	}
	var items []SpecItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	return items
}

// MarshalSpecItems encodes an item list for storage.
func MarshalSpecItems(items []SpecItem) string {
	b, _ := json.Marshal(items)
	return string(b)
}

