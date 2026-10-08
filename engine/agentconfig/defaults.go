package agentconfig

import (
	"embed"
	"fmt"
	"io/fs"
	"sync"

	"gopkg.in/yaml.v3"
)

// The built-in roles are embedded so a deployment does not depend on the
// checkout being present. They are copied into database Agent rows when a
// company is created or when a later release adds a role.
//
//go:embed agent_configs/*.yaml
var builtinAgentFiles embed.FS

var (
	builtinOnce    sync.Once
	builtinCatalog []*AgentConfig
)

// BuiltinConfigs returns the built-in roles in filename order; the numeric
// filename prefixes define the canonical order.
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
			var cfg AgentConfig
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				panic(fmt.Sprintf("invalid embedded built-in agent config %s: %v", path, err))
			}
			catalog = append(catalog, &cfg)
		}
		builtinCatalog = catalog
	})
	return builtinCatalog
}
