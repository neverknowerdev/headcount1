package engine

import (
	"encoding/json"
	"strings"

	"agent-orchestrator/engine/aicli"
)

// WorkerPolicy is deliberately a small, forward-compatible JSON contract.
// inherit preserves the parent's effective access, deny removes only the
// listed entries, and custom limits the worker to the listed entries.
type WorkerPolicy struct {
	Mode    string   `json:"mode"`
	Denied  []string `json:"denied,omitempty"`
	Allowed []string `json:"allowed,omitempty"`
}

func parseWorkerPolicy(raw string) WorkerPolicy {
	policy := WorkerPolicy{Mode: "inherit"}
	if strings.TrimSpace(raw) == "" {
		return policy
	}
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return WorkerPolicy{Mode: "inherit"}
	}
	switch policy.Mode {
	case "deny", "custom":
		return policy
	default:
		policy.Mode = "inherit"
		return policy
	}
}

func applyWorkerToolPolicy(registry *aicli.Registry, parentPermissions, workerPermissions string) *aicli.Registry {
	// The parent's main policy is always a ceiling. The legacy permissions
	// format is a deny map, so it remains compatible with existing agents.
	registry = registry.Exclude(deniedFromPermissions(parentPermissions))
	policy := parseWorkerPolicy(workerPermissions)
	switch policy.Mode {
	case "deny":
		return registry.Exclude(policy.Denied)
	case "custom":
		// Workers must always retain the lifecycle/status tools needed to
		// report progress and finish, even when the configurable tools use a
		// strict custom allowlist.
		return registry.Filter(append(append([]string{}, policy.Allowed...), "finish_task", "finish_work", "report_status"))
	default:
		return registry
	}
}

func deniedFromPermissions(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var permissions map[string]string
	if json.Unmarshal([]byte(raw), &permissions) != nil {
		return nil
	}
	denied := make([]string, 0)
	for name, value := range permissions {
		if strings.EqualFold(strings.TrimSpace(value), "deny") {
			denied = append(denied, name)
		}
	}
	return denied
}

func workerMCPAllowed(name, parentAllowedMCPs, workerAllowedMCPs string) bool {
	if parent := decodeAgentNames(parentAllowedMCPs); len(parent) > 0 && !containsFold(parent, name) {
		return false
	}
	policy := parseWorkerPolicy(workerAllowedMCPs)
	switch policy.Mode {
	case "deny":
		return !containsFold(policy.Denied, name)
	case "custom":
		return containsFold(policy.Allowed, name)
	default:
		return true
	}
}

func containsFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(wanted)) {
			return true
		}
	}
	return false
}
