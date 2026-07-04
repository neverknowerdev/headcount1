package db

import "encoding/json"

// Spec item verification statuses. Kept for rendering tasks created before
// acceptance criteria became prompt-level judgement calls (the engine no
// longer writes these fields itself).
const (
	SpecItemPending = "pending"
	SpecItemPassed  = "passed"
	SpecItemFailed  = "failed"
)

// SpecItem is one acceptance criterion or test case. Stored as a JSON array
// in Task.AcceptanceCriteria / Task.TestCases so the UI can render each item
// individually instead of a wall of text.
type SpecItem struct {
	ID     int    `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"`
	Note   string `json:"note,omitempty"`
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
