package logging

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestLogger(t *testing.T) *ProxyLogger {
	t.Helper()
	l, err := NewProxyLogger(t.TempDir(), "acme", 1, 100)
	if err != nil {
		t.Fatalf("NewProxyLogger: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

// fakeHub records every event broadcast through it, for asserting on the
// shape of entries the frontend would receive over the WebSocket.
type fakeHub struct {
	events []map[string]interface{}
}

func (h *fakeHub) BroadcastEvent(name string, payload interface{}) {
	entry := payload.(map[string]interface{})["entry"].(map[string]interface{})
	h.events = append(h.events, entry)
}

func newTestLoggerWithHub(t *testing.T) (*ProxyLogger, *fakeHub) {
	t.Helper()
	hub := &fakeHub{}
	l, err := NewProxyLoggerWithHub(t.TempDir(), "acme", 1, 100, hub, nil)
	if err != nil {
		t.Fatalf("NewProxyLoggerWithHub: %v", err)
	}
	t.Cleanup(func() { l.Close() })
	return l, hub
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestStartSession_CreatesRunFolderAndFile(t *testing.T) {
	l := newTestLogger(t)

	if err := l.StartSession(SessionInfo{
		SessionID: 1,
		AgentName: "SmartPlanner",
		Role:      "orchestration",
		Model:     "gpt-5",
		Provider:  "OpenAI",
		Tools:     []string{"read", "write"},
		MCPs:      []string{"github"},
	}); err != nil {
		t.Fatalf("StartSession: %v", err)
	}

	entries, err := os.ReadDir(l.FilePath())
	if err != nil {
		t.Fatalf("read run dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "SmartPlanner-1.log" {
		t.Fatalf("expected exactly SmartPlanner-1.log in run dir, got %v", entries)
	}

	content := readFile(t, filepath.Join(l.FilePath(), "SmartPlanner-1.log"))
	for _, want := range []string{
		"Session ID: 1", "Agent: SmartPlanner", "Role: orchestration",
		"Model: gpt-5", "Provider: OpenAI", "Tools: read, write", "MCP servers: github",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("header missing %q; got:\n%s", want, content)
		}
	}
	if strings.Contains(content, "Parent session ID") {
		t.Errorf("main session should not have a Parent session ID line")
	}
}

func TestStartSession_SubSessionGetsOwnFileAndMarksMain(t *testing.T) {
	l := newTestLogger(t)

	if err := l.StartSession(SessionInfo{SessionID: 1, AgentName: "SmartPlanner", Role: "orchestration"}); err != nil {
		t.Fatalf("StartSession(main): %v", err)
	}
	l.LogInfo("main session doing work")

	parent := int32(1)
	if err := l.StartSession(SessionInfo{
		SessionID:       2,
		ParentSessionID: &parent,
		AgentName:       "Coder",
		Role:            "implementation",
	}); err != nil {
		t.Fatalf("StartSession(sub): %v", err)
	}
	l.LogInfo("sub session doing work")
	l.EndSession()
	l.LogInfo("back to main session")

	mainContent := readFile(t, filepath.Join(l.FilePath(), "SmartPlanner-1.log"))
	if !strings.Contains(mainContent, "starting new session: Coder (session #2, role=implementation)") {
		t.Errorf("main file missing sub-session start marker; got:\n%s", mainContent)
	}
	if !strings.Contains(mainContent, "main session doing work") {
		t.Errorf("main file missing its own info line; got:\n%s", mainContent)
	}
	if !strings.Contains(mainContent, "back to main session") {
		t.Errorf("main file missing post-EndSession info line (EndSession did not switch back); got:\n%s", mainContent)
	}
	if strings.Contains(mainContent, "sub session doing work") {
		t.Errorf("sub-session's info line leaked into the main file; got:\n%s", mainContent)
	}

	subContent := readFile(t, filepath.Join(l.FilePath(), "Coder-2.log"))
	if !strings.Contains(subContent, "Parent session ID: 1") {
		t.Errorf("sub-session file missing parent session ID; got:\n%s", subContent)
	}
	if !strings.Contains(subContent, "sub session doing work") {
		t.Errorf("sub-session file missing its own info line; got:\n%s", subContent)
	}
	if !strings.Contains(subContent, "Session ended") {
		t.Errorf("sub-session file missing 'Session ended' trailer after EndSession; got:\n%s", subContent)
	}
}

func TestBroadcast_TagsEntriesWithSessionID(t *testing.T) {
	l, hub := newTestLoggerWithHub(t)

	if err := l.StartSession(SessionInfo{SessionID: 1, AgentName: "SmartPlanner", Role: "orchestration"}); err != nil {
		t.Fatalf("StartSession(main): %v", err)
	}
	l.LogInfo("main activity")

	parent := int32(1)
	if err := l.StartSession(SessionInfo{
		SessionID:       2,
		ParentSessionID: &parent,
		AgentName:       "Coder",
		Role:            "implementation",
		Model:           "gpt-5",
	}); err != nil {
		t.Fatalf("StartSession(sub): %v", err)
	}
	l.LogInfo("sub activity")
	l.EndSession()

	var mainInfo, sessionStart, subInfo, sessionEnd map[string]interface{}
	for _, e := range hub.events {
		switch e["type"] {
		case "info":
			if e["content"] == "main activity" {
				mainInfo = e
			} else if e["content"] == "sub activity" {
				subInfo = e
			}
		case "session_start":
			sessionStart = e
		case "session_end":
			sessionEnd = e
		}
	}

	if mainInfo == nil || mainInfo["session_id"] != int32(1) {
		t.Fatalf("main info entry missing session_id=1: %+v", mainInfo)
	}
	if mainInfo["parent_session_id"] != nil {
		t.Errorf("main info entry should have no parent_session_id: %+v", mainInfo)
	}

	// The session_start marker itself is tagged with the PARENT's session_id
	// (so it renders inline in the main session's timeline), while carrying
	// started_session_id for the child it announces.
	if sessionStart == nil || sessionStart["session_id"] != int32(1) {
		t.Fatalf("session_start entry should be tagged with parent session_id=1: %+v", sessionStart)
	}
	var meta map[string]interface{}
	if err := json.Unmarshal([]byte(sessionStart["content"].(string)), &meta); err != nil {
		t.Fatalf("session_start content not valid JSON: %v", err)
	}
	if int32(meta["started_session_id"].(float64)) != 2 {
		t.Errorf("session_start content missing started_session_id=2: %+v", meta)
	}
	if meta["agent_name"] != "Coder" || meta["model"] != "gpt-5" {
		t.Errorf("session_start content missing agent/model: %+v", meta)
	}

	if subInfo == nil || subInfo["session_id"] != int32(2) {
		t.Fatalf("sub info entry missing session_id=2: %+v", subInfo)
	}
	if subInfo["parent_session_id"] != int32(1) {
		t.Errorf("sub info entry missing parent_session_id=1: %+v", subInfo)
	}

	if sessionEnd == nil || sessionEnd["session_id"] != int32(2) {
		t.Fatalf("session_end entry should be tagged with the ending session_id=2: %+v", sessionEnd)
	}
}

func TestLogInfo_WithoutStartSession_DoesNotPanic(t *testing.T) {
	l := newTestLogger(t)
	// No StartSession called — Log* calls should silently no-op on the file
	// side (io.Discard) rather than crash.
	l.LogInfo("no session yet")
}

func TestSanitizeFileName(t *testing.T) {
	cases := map[string]string{
		"SmartPlanner":       "SmartPlanner",
		"":                   "agent",
		"Coder/Tester":       "Coder_Tester",
		"a b c":              "a_b_c",
		"weird!!@#name.here": "weird_name.here",
	}
	for in, want := range cases {
		if got := sanitizeFileName(in); got != want {
			t.Errorf("sanitizeFileName(%q) = %q, want %q", in, got, want)
		}
	}
}
