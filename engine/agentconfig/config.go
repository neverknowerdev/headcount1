// Package agentconfig holds the built-in agent roles. A role is only a name,
// a description and a one-line prompt; the user extends the prompt from the
// UI, and everything about how work is done belongs to the task workflow.
package agentconfig

// AgentConfig is one built-in role as it is checked in.
type AgentConfig struct {
	// Name identifies the role and is its display name.
	Name string `yaml:"name"`
	// ShortName is the compact label used in run keys, e.g. "DEC-50-CTO".
	ShortName string `yaml:"short_name"`
	// Description says what the role is for.
	Description string `yaml:"description"`
	// Prompt is the role line a company's agent starts with.
	Prompt string `yaml:"prompt"`
}
