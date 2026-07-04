package aicli

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// transcriptToolResultMaxChars truncates tool-result content before it is
// written to the transcript — miner input hygiene: verbatim recall needs the
// signal in a result, not megabytes of raw dump.
const transcriptToolResultMaxChars = 4096

// TranscriptWriter appends the session's conversation to a JSONL file in the
// Claude-Code session shape that `mempalace mine --mode convos` consumes:
// one {"type","message","ts","session_id"} object per line, where message
// content uses text / tool_use / tool_result blocks. The file is the
// mechanical no-loss capture layer — everything the model said and every tool
// result lands here before history pruning can drop it.
//
// Methods are safe for concurrent use; every append is flushed to disk.
type TranscriptWriter struct {
	mu       sync.Mutex
	file     *os.File
	session  string
	appended int
}

// NewTranscriptWriter opens (or creates) the transcript file at path.
func NewTranscriptWriter(path, sessionID string) (*TranscriptWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	return &TranscriptWriter{file: f, session: sessionID}, nil
}

// Append writes one history message as a transcript line. System messages are
// skipped (prompt boilerplate, not conversation). Errors are swallowed after
// the file is gone — transcript capture must never fail a run.
func (w *TranscriptWriter) Append(msg Message) {
	if w == nil {
		return
	}
	line := transcriptLine(msg, w.session)
	if line == nil {
		return
	}
	data, err := json.Marshal(line)
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return
	}
	if _, err := w.file.Write(append(data, '\n')); err == nil {
		w.appended++
		w.file.Sync()
	}
}

// Count returns how many messages have been written so far. The engine uses
// it to skip redundant mine calls when nothing new was captured.
func (w *TranscriptWriter) Count() int {
	if w == nil {
		return 0
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.appended
}

// Close flushes and closes the underlying file.
func (w *TranscriptWriter) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		w.file.Close()
		w.file = nil
	}
}

// transcriptLine converts a history message to the miner's JSONL shape, or
// nil when the message carries nothing worth capturing.
func transcriptLine(msg Message, sessionID string) map[string]any {
	var msgType string
	var content any

	switch msg.Role {
	case "user":
		if msg.Content == "" {
			return nil
		}
		msgType, content = "user", msg.Content
	case "assistant":
		blocks := []map[string]any{}
		if msg.Content != "" {
			blocks = append(blocks, map[string]any{"type": "text", "text": msg.Content})
		}
		for _, tc := range msg.ToolCalls {
			var input map[string]any
			if json.Unmarshal([]byte(tc.Function.Arguments), &input) != nil {
				input = map[string]any{}
			}
			blocks = append(blocks, map[string]any{
				"type": "tool_use", "id": tc.ID, "name": tc.Function.Name, "input": input,
			})
		}
		if len(blocks) == 0 {
			return nil
		}
		msgType, content = "assistant", blocks
	case "tool":
		out := msg.Content
		if len(out) > transcriptToolResultMaxChars {
			out = out[:transcriptToolResultMaxChars] + "\n…[truncated for transcript]"
		}
		msgType = "user" // the miner files tool results under the preceding assistant turn
		content = []map[string]any{{
			"type": "tool_result", "tool_use_id": msg.ToolCallID, "content": out,
		}}
	default: // system etc.
		return nil
	}

	return map[string]any{
		"type":       msgType,
		"message":    map[string]any{"role": msgType, "content": content},
		"ts":         time.Now().UTC().Format(time.RFC3339),
		"session_id": sessionID,
	}
}
