package endpoints

import (
	"strings"
	"testing"
)

func TestCategorizeMCPErrorDoesNotPresentGitHubMCPAsGitRequirement(t *testing.T) {
	message := categorizeMCPError("github", `exec: "github-mcp-server": executable file not found in $PATH`)

	if strings.Contains(message, "brew install") || strings.Contains(message, "Binary not installed") {
		t.Fatalf("GitHub MCP should be presented as an optional deployment feature, got %q", message)
	}
	if !strings.Contains(message, "Git clone, pull, push") || !strings.Contains(message, "unaffected") {
		t.Fatalf("message should distinguish native Git operations from optional MCP tools, got %q", message)
	}
}
