package aicli_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"agent-orchestrator/engine/aicli"
)

type listingStubTool struct {
	name string
	desc string
}

func (t *listingStubTool) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        t.name,
			Description: t.desc,
			Parameters:  json.RawMessage(`{"type":"object","properties":{}}`),
		},
	}
}

func (t *listingStubTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return "", nil
}

func TestRegistry_PromptListing_Empty(t *testing.T) {
	assert.Empty(t, aicli.NewRegistry().PromptListing())
}

func TestRegistry_PromptListing(t *testing.T) {
	reg := aicli.NewRegistry()
	reg.Register(&listingStubTool{name: "write", desc: "Write a file. Overwrites existing content."})
	reg.Register(&listingStubTool{name: "bash", desc: "Run a shell command in the workspace."})
	reg.Register(&listingStubTool{name: "read", desc: strings.Repeat("x", 200)})

	listing := reg.PromptListing()

	assert.Contains(t, listing, "## Available tools")
	assert.Contains(t, listing, "ONLY tools you can call")

	// Sorted order: bash, read, write.
	iBash := strings.Index(listing, "- bash")
	iRead := strings.Index(listing, "- read")
	iWrite := strings.Index(listing, "- write")
	assert.True(t, iBash >= 0 && iBash < iRead && iRead < iWrite, "tools must be listed in sorted order")

	// Description trimmed to the first sentence.
	assert.Contains(t, listing, "- write — Write a file.\n")
	assert.NotContains(t, listing, "Overwrites existing content")

	// Long sentence-less descriptions are capped.
	assert.Contains(t, listing, strings.Repeat("x", 140)+"…")
	assert.NotContains(t, listing, strings.Repeat("x", 141))
}
