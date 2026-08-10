package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/agentconfig"
	"agent-orchestrator/engine/aicli"
)

// canonicalAgentRole returns the stable role key for a built-in template or
// an empty string for a custom role. It is used only while bootstrapping legacy
// rows; the returned RoleKey is persisted on the Agent and is authoritative for
// later runs.
func canonicalAgentRole(name string) string {
	normalized := strings.ToLower(strings.TrimSpace(name))
	if normalized == "ceo agent" {
		return "CEO"
	}
	for _, cfg := range agentconfig.BuiltinConfigs() {
		if strings.EqualFold(strings.TrimSpace(cfg.Name), normalized) {
			return cfg.Name
		}
	}
	return ""
}

func templateForRole(role string) *agentconfig.AgentConfig {
	for _, cfg := range agentconfig.BuiltinConfigs() {
		if strings.EqualFold(strings.TrimSpace(cfg.Name), strings.TrimSpace(role)) {
			return cfg
		}
	}
	return nil
}

func encodeAgentNames(names []string) string {
	if len(names) == 0 {
		return ""
	}
	b, err := json.Marshal(names)
	if err != nil {
		return ""
	}
	return string(b)
}

func decodeAgentNames(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var names []string
	if err := json.Unmarshal([]byte(raw), &names); err != nil {
		return nil
	}
	return names
}

// templatePermissions converts a file template's allowlist into the legacy
// database deny-map format used by the UI. This is a one-time bootstrap
// representation; runtime reads only Agent.Permissions.
func templatePermissions(cfg *agentconfig.AgentConfig) string {
	if cfg == nil {
		return ""
	}
	all := aicli.Names(
		aicli.ToolBash,
		aicli.ToolRead,
		aicli.ToolWrite,
		aicli.ToolListDir,
		aicli.ToolGrep,
		aicli.ToolWebFetch,
		aicli.ToolBrowserUse,
		aicli.ToolFinishTask,
		aicli.ToolWriteArtifact,
		aicli.ToolListArtifacts,
		aicli.ToolReadArtifact,
		aicli.ToolAskArtifact,
		aicli.ToolCreateSubtask,
		aicli.ToolAnswerSubtaskQuestion,
		aicli.ToolAskTaskOwner,
		aicli.ToolCreateTask,
		aicli.ToolAskHuman,
		aicli.ToolReportStatus,
		aicli.ToolCallMCP,
		aicli.ToolDiscoverMCP,
		aicli.ToolCodegraphWildcard,
	)
	permissions := make(map[string]string)
	for _, name := range all {
		if !cfg.IsToolAllowed(name) {
			permissions[name] = "deny"
		}
	}
	if len(permissions) == 0 {
		return ""
	}
	b, err := json.Marshal(permissions)
	if err != nil {
		return ""
	}
	return string(b)
}

// applyAgentTemplate fills missing database fields from a built-in template.
// Existing user values, especially prompt/model/permissions, are never
// overwritten. Templates are therefore bootstrap input, not runtime config.
func applyAgentTemplate(agent db.Agent, role string, cfg *agentconfig.AgentConfig, subagent bool) (db.Agent, bool) {
	if cfg == nil {
		return agent, false
	}
	changed := false
	if agent.RoleKey == "" {
		agent.RoleKey = cfg.Name
		changed = true
	}
	if agent.ShortName == "" {
		agent.ShortName = cfg.EffectiveShortName()
		changed = true
	}
	if agent.Description == "" {
		agent.Description = cfg.Description
		changed = true
	}
	if agent.SystemPrompt == "" {
		agent.SystemPrompt = cfg.Prompt
		changed = true
	}
	if agent.ChatType == "" {
		agent.ChatType = string(cfg.ChatType)
		changed = true
	}
	if agent.ReasoningLevel == "" {
		agent.ReasoningLevel = string(cfg.ReasoningLevel)
		changed = true
	}
	if agent.Subagents == "" {
		agent.Subagents = encodeAgentNames(cfg.Subagents)
		changed = true
	}
	if agent.AllowedMCPs == "" {
		agent.AllowedMCPs = encodeAgentNames(cfg.AllowedMCPs)
		if cfg.AllowedMCPs != nil {
			changed = true
		}
	}
	if agent.Permissions == "" {
		agent.Permissions = templatePermissions(cfg)
		changed = true
	}
	if agent.Mode == "" {
		if subagent {
			agent.Mode = "subagent"
		} else {
			agent.Mode = "primary"
		}
		changed = true
	}
	_ = role // role is kept explicit at call sites for readability.
	return agent, changed
}

func (e *NativeEngine) persistAgentTemplate(ctx context.Context, agent db.Agent, role string, subagent bool) (db.Agent, error) {
	cfg := templateForRole(role)
	if cfg == nil {
		return agent, nil
	}
	updated, changed := applyAgentTemplate(agent, role, cfg, subagent)
	if !changed {
		return updated, nil
	}
	saved, err := e.q.UpdateAgent(ctx, updated)
	if err != nil {
		return db.Agent{}, fmt.Errorf("save agent %q settings: %w", updated.Name, err)
	}
	return saved, nil
}

// ensureAgentForRole resolves a delegated role exclusively to a database Agent
// row. If a legacy company has no row yet, the file template is copied once
// into a new row using the parent's provider/model as its initial target.
func (e *NativeEngine) ensureAgentForRole(ctx context.Context, companyID int32, role string, fallback db.Agent) (db.Agent, error) {
	requested := strings.TrimSpace(role)
	if requested == "" {
		return db.Agent{}, fmt.Errorf("agent role/name is empty")
	}
	builtinRole := canonicalAgentRole(requested)
	agents, err := e.q.ListAgentsByCompany(ctx, companyID)
	if err != nil {
		return db.Agent{}, fmt.Errorf("list agents for %q: %w", requested, err)
	}
	for _, candidate := range agents {
		if strings.EqualFold(strings.TrimSpace(candidate.RoleKey), requested) ||
			strings.EqualFold(strings.TrimSpace(candidate.Name), requested) ||
			(builtinRole != "" && canonicalAgentRole(candidate.Name) == builtinRole) {
			if builtinRole == "" {
				return candidate, nil
			}
			return e.persistAgentTemplate(ctx, candidate, builtinRole, candidate.ID != fallback.ID)
		}
	}
	if builtinRole == "" {
		return db.Agent{}, fmt.Errorf("no database agent matches %q", requested)
	}
	role = builtinRole
	cfg := templateForRole(role)
	if cfg == nil {
		return db.Agent{}, fmt.Errorf("no database agent or template exists for role %q", role)
	}
	base := fallback
	if base.ProviderID == nil && base.ModelGroupID == nil {
		for _, candidate := range agents {
			if candidate.ProviderID != nil || candidate.ModelGroupID != nil {
				base = candidate
				break
			}
		}
	}
	created := db.Agent{
		CompanyID:      companyID,
		Name:           cfg.Name,
		RoleKey:        cfg.Name,
		ShortName:      cfg.EffectiveShortName(),
		Description:    cfg.Description,
		SystemPrompt:   cfg.Prompt,
		ProviderID:     base.ProviderID,
		ModelGroupID:   base.ModelGroupID,
		Model:          base.Model,
		Mode:           "subagent",
		ChatType:       string(cfg.ChatType),
		ReasoningLevel: string(cfg.ReasoningLevel),
		Subagents:      encodeAgentNames(cfg.Subagents),
		AllowedMCPs:    encodeAgentNames(cfg.AllowedMCPs),
		Permissions:    templatePermissions(cfg),
	}
	created, err = e.q.CreateAgent(ctx, created)
	if err != nil {
		return db.Agent{}, fmt.Errorf("create database agent for role %q: %w", role, err)
	}
	return created, nil
}

// prepareTaskAgent initializes legacy Agent rows from templates and repairs
// legacy child tasks whose AgentID still points at the parent. AgentID is the
// only field used for current execution after this boundary.
func (e *NativeEngine) prepareTaskAgent(ctx context.Context, task *db.Task) (db.Agent, error) {
	if task.AgentID == nil {
		return db.Agent{}, fmt.Errorf("task %d has no agent", task.ID)
	}
	agent, err := e.q.GetAgent(ctx, *task.AgentID)
	if err != nil {
		return db.Agent{}, err
	}
	role := agent.RoleKey
	if task.ParentID != nil && task.AgentConfigName != "" &&
		!strings.EqualFold(strings.TrimSpace(agent.RoleKey), strings.TrimSpace(task.AgentConfigName)) {
		target, targetErr := e.ensureAgentForRole(ctx, task.CompanyID, task.AgentConfigName, agent)
		if targetErr != nil {
			return db.Agent{}, targetErr
		}
		if target.ID != agent.ID {
			task.AgentID = &target.ID
			if _, updateErr := e.q.UpdateTask(ctx, *task); updateErr != nil {
				return db.Agent{}, fmt.Errorf("repair task %d agent: %w", task.ID, updateErr)
			}
		}
		agent = target
		role = target.RoleKey
	}
	if role == "" {
		role = canonicalAgentRole(agent.Name)
	}
	if role == "" && task.ParentID == nil {
		role = "CEO"
	}
	if role != "" {
		agent, err = e.persistAgentTemplate(ctx, agent, role, task.ParentID != nil)
		if err != nil {
			return db.Agent{}, err
		}
	}
	return agent, nil
}
