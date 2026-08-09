package engine

import (
	"encoding/json"
	"strings"

	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
)

// applyStoredToolPermissions translates the legacy/UI permission labels to
// native registry names. The UI treats an omitted key as allowed, so only
// explicit "deny" values remove tools. Lifecycle tools remain available even
// when the UI has no corresponding checkbox; otherwise an agent could never
// finish its task or report progress.
func applyStoredToolPermissions(registry *aicli.Registry, raw string) (*aicli.Registry, error) {
	var permissions map[string]string
	if err := json.Unmarshal([]byte(raw), &permissions); err != nil {
		return registry, err
	}
	aliases := map[string][]string{
		"bash":        {string(tools.ToolBash)},
		"read":        {string(tools.ToolRead)},
		"edit":        {string(tools.ToolWrite)},
		"glob":        {string(tools.ToolListDir)},
		"grep":        {string(tools.ToolGrep)},
		"webfetch":    {string(tools.ToolWebFetch)},
		"websearch":   {string(tools.ToolWebFetch)},
		"task":        {string(tools.ToolCreateSubtask), string(tools.ToolCreateTask), string(tools.ToolAnswerSubtaskQuestion), string(tools.ToolAskTaskOwner)},
		"write":       {string(tools.ToolWrite)},
		"ls":          {string(tools.ToolListDir)},
		"web_fetch":   {string(tools.ToolWebFetch)},
		"create_task": {string(tools.ToolCreateTask)},
	}
	var denied []string
	for label, names := range aliases {
		if strings.EqualFold(strings.TrimSpace(permissions[label]), "deny") {
			denied = append(denied, names...)
		}
	}
	return registry.Exclude(denied), nil
}
