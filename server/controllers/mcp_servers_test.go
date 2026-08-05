package endpoints

import (
	"strings"
	"testing"
)

func TestCategorizeMCPErrorLinksMissingGitHubMCPToSystemSetup(t *testing.T) {
	message := categorizeMCPError("github", `exec: "github-mcp-server": executable file not found in $PATH`)

	if !strings.Contains(message, "installation failed") || !strings.Contains(message, "system setup error") {
		t.Fatalf("missing binary should point to the deployment setup failure, got %q", message)
	}
}
