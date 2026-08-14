package aicli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// LoadMessageHistory reads the canonical message events from a run's JSONL
// trajectory. maxSequence is the checkpoint cursor; events after it belong to
// a later attempt and are ignored. Non-message log entries are deliberately
// ignored because they are observability data, not conversation state.
func LoadMessageHistory(path string, maxSequence int64) ([]Message, error) {
	history, _, err := LoadMessageHistoryWithCursor(path, maxSequence)
	return history, err
}

// LoadMessageHistoryWithCursor reads message events and returns the highest
// message sequence included in the result. A zero maxSequence means "read the
// complete log" and is used when recovering a run that failed before a
// planned checkpoint was persisted.
func LoadMessageHistoryWithCursor(path string, maxSequence int64) ([]Message, int64, error) {
	if path == "" {
		return nil, 0, fmt.Errorf("message history log path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open message history log: %w", err)
	}
	defer file.Close()

	type logEntry struct {
		Type    string          `json:"type"`
		Seq     json.Number     `json:"seq"`
		Content json.RawMessage `json:"content"`
	}
	var history []Message
	var highestSequence int64
	scanner := bufio.NewScanner(file)
	// A message can contain a large tool result. The canonical event should be
	// read losslessly rather than using Scanner's small default token limit.
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		var entry logEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			return nil, 0, fmt.Errorf("parse message history log line %d: %w", line, err)
		}
		if entry.Type != "message" {
			continue
		}
		sequence, err := entry.Seq.Int64()
		if err != nil {
			return nil, 0, fmt.Errorf("message history log line %d has invalid sequence: %w", line, err)
		}
		if maxSequence > 0 && sequence > maxSequence {
			continue
		}
		if sequence > highestSequence {
			highestSequence = sequence
		}
		var messageJSON string
		if err := json.Unmarshal(entry.Content, &messageJSON); err != nil {
			return nil, 0, fmt.Errorf("parse message history log line %d: %w", line, err)
		}
		var message Message
		if err := json.Unmarshal([]byte(messageJSON), &message); err != nil {
			return nil, 0, fmt.Errorf("parse message history log line %d: %w", line, err)
		}
		history = append(history, message)
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, fmt.Errorf("read message history log: %w", err)
	}
	if len(history) == 0 {
		return nil, 0, fmt.Errorf("message history log contains no messages through sequence %d", maxSequence)
	}
	return history, highestSequence, nil
}
