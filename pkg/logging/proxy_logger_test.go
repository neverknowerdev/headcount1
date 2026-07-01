package logging

import (
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
