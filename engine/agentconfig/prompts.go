package agentconfig

import (
	"embed"
	"fmt"
)

// promptFiles keeps every model-facing prompt in the prompts tree so runtime
// code only supplies data and never becomes an accidental prompt registry.
//
//go:embed prompts/*.md prompts/utils/*.md
var promptFiles embed.FS

// MustPrompt returns a checked-in prompt asset and panics during startup if a
// required prompt was accidentally removed or renamed.
func MustPrompt(name string) string {
	content, err := promptFiles.ReadFile("prompts/" + name)
	if err != nil {
		panic(fmt.Sprintf("agentconfig prompt %q: %v", name, err))
	}
	return string(content)
}
