package aicli_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/pkg/secrets/redact"
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

type secretStubTool struct {
	name   string
	output string
	err    error
}

func (t *secretStubTool) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:       t.name,
			Parameters: json.RawMessage(`{"type":"object","properties":{}}`),
		},
	}
}

func (t *secretStubTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	return t.output, t.err
}

// TestRegistry_Execute_ScrubsSecrets: a tool whose output contains a
// registered secret (e.g. bash cat-ing a .env) must not be able to place
// that secret into the message history handed back to the agent loop.
func TestRegistry_Execute_ScrubsSecrets(t *testing.T) {
	const secret = "sk-exectest-supersecret-98765432"
	redact.Register("exec-test", secret)

	reg := aicli.NewRegistry()
	reg.Register(&secretStubTool{name: "leaky", output: "API key is " + secret})
	reg.Register(&secretStubTool{name: "failing", err: errors.New("dial with key " + secret + " refused")})

	out, err := reg.Execute(context.Background(), "leaky", nil)
	assert.NoError(t, err)
	assert.NotContains(t, out, secret)
	assert.Contains(t, out, "[REDACTED:exec-test]")

	_, err = reg.Execute(context.Background(), "failing", nil)
	assert.Error(t, err)
	assert.NotContains(t, err.Error(), secret)
}
