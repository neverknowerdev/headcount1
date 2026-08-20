package agentconfig

import (
	"embed"
	"fmt"
	"io/fs"
	"sync"
)

// The built-in catalog is deliberately data-driven. It is embedded into the
// binary so a deployment does not depend on the checkout being present, while
// keeping prompts, hierarchy, and initial tool policy easy to review and edit.
// These values are copied into database Agent rows when a company is created
// or when a new built-in role is added in a later release.
//
//go:embed agent_configs/*.yaml
var builtinAgentFiles embed.FS

var (
	builtinOnce    sync.Once
	builtinCatalog []*AgentConfig
)

// BuiltinConfigs returns the predefined agent configurations in lexicographic
// filename order. The numeric filename prefixes define the canonical order.
func BuiltinConfigs() []*AgentConfig {
	builtinOnce.Do(func() {
		paths, err := fs.Glob(builtinAgentFiles, "agent_configs/*.yaml")
		if err != nil {
			panic(fmt.Sprintf("invalid embedded built-in agent config pattern: %v", err))
		}

		catalog := make([]*AgentConfig, 0, len(paths))
		for _, path := range paths {
			data, err := builtinAgentFiles.ReadFile(path)
			if err != nil {
				panic(fmt.Sprintf("read embedded built-in agent config %s: %v", path, err))
			}
			cfg, err := LoadYAMLFromBytes(data, "")
			if err != nil {
				panic(fmt.Sprintf("invalid embedded built-in agent config %s: %v", path, err))
			}
			catalog = append(catalog, cfg)
		}
		builtinCatalog = catalog
	})
	return builtinCatalog
}

// builtinConfigs preserves the internal factory hook used by older callers.
func builtinConfigs() []*AgentConfig { return BuiltinConfigs() }
