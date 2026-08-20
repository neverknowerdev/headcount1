package agentconfig

import (
	_ "embed"
	"fmt"
	"sync"

	"gopkg.in/yaml.v3"
)

// The built-in catalog is deliberately data-driven. It is embedded into the
// binary so a deployment does not depend on the checkout being present, while
// keeping prompts, hierarchy, and initial tool policy easy to review and edit.
// These values are copied into database Agent rows when a company is created
// or when a new built-in role is added in a later release.
//
//go:embed builtin_agents.yaml
var builtinAgentsYAML []byte

var (
	builtinOnce    sync.Once
	builtinCatalog []*AgentConfig
)

// BuiltinConfigs returns the predefined agent configurations in their
// canonical YAML order.
func BuiltinConfigs() []*AgentConfig {
	builtinOnce.Do(func() {
		var catalog struct {
			Agents []*AgentConfig `yaml:"agents"`
		}
		if err := yaml.Unmarshal(builtinAgentsYAML, &catalog); err != nil {
			panic(fmt.Sprintf("invalid embedded built-in agent catalog: %v", err))
		}
		builtinCatalog = catalog.Agents
	})
	return builtinCatalog
}

// builtinConfigs preserves the internal factory hook used by older callers.
func builtinConfigs() []*AgentConfig { return BuiltinConfigs() }
