package aicli

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTranscriptLineFormat pins the JSONL shape consumed by
// `mempalace mine --mode convos` (Claude-Code session format).
func TestTranscriptLineFormat(t *testing.T) {
	user := transcriptLine(Message{Role: "user", Content: "do the thing"}, "run-7")
	if user["type"] != "user" || user["session_id"] != "run-7" {
		t.Errorf("user line = %#v", user)
	}
	if msg := user["message"].(map[string]any); msg["content"] != "do the thing" {
		t.Errorf("user content = %#v", msg["content"])
	}

	asst := transcriptLine(Message{
		Role:    "assistant",
		Content: "on it",
		ToolCalls: []ToolCall{{
			ID: "call_1", Type: "function",
			Function: FuncCall{Name: "grep", Arguments: `{"pattern":"x"}`},
		}},
	}, "run-7")
	blocks := asst["message"].(map[string]any)["content"].([]map[string]any)
	if len(blocks) != 2 || blocks[0]["type"] != "text" || blocks[1]["type"] != "tool_use" {
		t.Fatalf("assistant blocks = %#v", blocks)
	}
	if blocks[1]["name"] != "grep" || blocks[1]["id"] != "call_1" {
		t.Errorf("tool_use block = %#v", blocks[1])
	}

	tool := transcriptLine(Message{Role: "tool", ToolCallID: "call_1", Content: "match found"}, "run-7")
	// Tool results are filed as user-type messages with tool_result blocks —
	// the miner merges them into the preceding assistant turn.
	if tool["type"] != "user" {
		t.Errorf("tool line type = %v", tool["type"])
	}
	tb := tool["message"].(map[string]any)["content"].([]map[string]any)
	if tb[0]["type"] != "tool_result" || tb[0]["tool_use_id"] != "call_1" {
		t.Errorf("tool_result block = %#v", tb[0])
	}

	if transcriptLine(Message{Role: "system", Content: "prompt"}, "run-7") != nil {
		t.Error("system messages must be skipped")
	}
	if transcriptLine(Message{Role: "assistant"}, "run-7") != nil {
		t.Error("empty assistant messages must be skipped")
	}
}

func TestTranscriptTruncatesToolResults(t *testing.T) {
	big := strings.Repeat("y", transcriptToolResultMaxChars+500)
	line := transcriptLine(Message{Role: "tool", ToolCallID: "c", Content: big}, "s")
	blocks := line["message"].(map[string]any)["content"].([]map[string]any)
	got := blocks[0]["content"].(string)
	if len(got) > transcriptToolResultMaxChars+100 {
		t.Errorf("tool result not truncated: %d chars", len(got))
	}
	if !strings.Contains(got, "truncated") {
		t.Error("truncation marker missing")
	}
}

func TestTranscriptWriterAppendsJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript-1.jsonl")
	w, err := NewTranscriptWriter(path, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	w.Append(Message{Role: "system", Content: "skipped"})
	w.Append(Message{Role: "user", Content: "hello"})
	w.Append(Message{Role: "assistant", Content: "hi"})
	w.Append(Message{Role: "tool", ToolCallID: "c1", Content: "result"})

	if w.Count() != 3 {
		t.Errorf("Count = %d, want 3 (system skipped)", w.Count())
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lines := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var parsed map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &parsed); err != nil {
			t.Errorf("line %d is not valid JSON: %v", lines, err)
		}
		for _, key := range []string{"type", "message", "ts", "session_id"} {
			if _, ok := parsed[key]; !ok {
				t.Errorf("line %d missing %q", lines, key)
			}
		}
		lines++
	}
	if lines != 3 {
		t.Errorf("file has %d lines, want 3", lines)
	}

	// Nil receiver is safe (transcript capture is optional).
	var nilW *TranscriptWriter
	nilW.Append(Message{Role: "user", Content: "x"})
	if nilW.Count() != 0 {
		t.Error("nil writer Count should be 0")
	}
	nilW.Close()
}
