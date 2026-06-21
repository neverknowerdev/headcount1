package agentconfig

import (
	"fmt"
	"sync"
)

// Factory manages named agent configurations and supports dynamic registration.
type Factory interface {
	// GetConfig returns the named configuration, or an error if not found.
	GetConfig(name string) (*AgentConfig, error)
	// Register adds or replaces a configuration (keyed on cfg.Name).
	Register(cfg *AgentConfig)
	// ListNames returns all registered configuration names.
	ListNames() []string
}

// DefaultFactory is a thread-safe Factory backed by an in-memory map,
// pre-loaded with the built-in agent configurations.
type DefaultFactory struct {
	mu      sync.RWMutex
	configs map[string]*AgentConfig
}

// NewDefaultFactory returns a DefaultFactory pre-loaded with all built-in
// agent configs (CEO, CTO, Programmer, QA, Writer, Researcher).
func NewDefaultFactory() *DefaultFactory {
	f := &DefaultFactory{configs: make(map[string]*AgentConfig)}
	for _, cfg := range builtinConfigs() {
		f.Register(cfg)
	}
	return f
}

// NewEmptyFactory returns a DefaultFactory with no pre-loaded configs.
func NewEmptyFactory() *DefaultFactory {
	return &DefaultFactory{configs: make(map[string]*AgentConfig)}
}

func (f *DefaultFactory) GetConfig(name string) (*AgentConfig, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	cfg, ok := f.configs[name]
	if !ok {
		return nil, fmt.Errorf("agent config %q not found", name)
	}
	return cfg, nil
}

func (f *DefaultFactory) Register(cfg *AgentConfig) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configs[cfg.Name] = cfg
}

func (f *DefaultFactory) ListNames() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	names := make([]string, 0, len(f.configs))
	for name := range f.configs {
		names = append(names, name)
	}
	return names
}
