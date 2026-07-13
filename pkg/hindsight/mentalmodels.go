package hindsight

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"agent-orchestrator/db"
)

// Mental models are Hindsight's standing, auto-refreshing syntheses: instead
// of an agent piecing together scattered facts on every recall, a mental
// model runs the reasoning once in the background and is fetched afterward
// as a cheap key-value lookup — cheap enough to do on every request. See
// docs/memory-layer-upgrade-plan.md Phase 3.
//
// Every model uses a deterministic id (allowed by Hindsight for custom ids),
// so no ID-lookup table is needed — the id is derived from the same
// project/agent/company identifiers used everywhere else in this package.
const (
	projectStateModelMaxTokens  = 2048
	agentPlaybookModelMaxTokens = 1024
	openBlockersModelMaxTokens  = 1024
)

func projectStateModelID(projectID int32) string { return fmt.Sprintf("project-state-%d", projectID) }
func agentPlaybookModelID(role string) string {
	return "agent-playbook-" + strings.ToLower(strings.ReplaceAll(role, " ", "-"))
}

const openBlockersModelID = "open-blockers"

// ensureModelOnce guards each (bank, modelID) pair so its create call only
// fires once per Service lifetime; a 404 GET means "not created yet".
func (s *Service) ensureModelOnce(bank, modelID string, create func()) {
	key := bank + "/" + modelID
	if _, done := s.ensuredModels.LoadOrStore(key, struct{}{}); done {
		return
	}
	create()
}

// EnsureProjectStateModel creates (if missing) the per-project synthesis of
// current implementation state, decisions and blockers, scoped to that
// project's tag so it draws on both its docs and its run experience.
func (s *Service) EnsureProjectStateModel(ctx context.Context, company db.Company, project db.Project) {
	c := s.client()
	if c == nil {
		return
	}
	bank := BankID(company.ID)
	modelID := projectStateModelID(project.ID)
	s.ensureModelOnce(bank, modelID, func() {
		s.createMentalModelIfMissing(ctx, bank, CreateMentalModelRequest{
			ID:   modelID,
			Name: fmt.Sprintf("Project state: %s", project.Name),
			SourceQuery: "What is the current implementation state of this project? Cover: what is implemented " +
				"and working, key technical decisions, current blockers or failures, and what was attempted but " +
				"not completed.",
			Tags:                      []string{ProjectTag(project.Name)},
			MaxTokens:                 projectStateModelMaxTokens,
			RefreshAfterConsolidation: true,
		})
	})
}

// EnsureAgentPlaybookModel creates (if missing) the per-role synthesis of
// working approaches and recurring mistakes, so an agent stops repeating
// known failure modes across unrelated tasks.
func (s *Service) EnsureAgentPlaybookModel(ctx context.Context, company db.Company, role string) {
	c := s.client()
	if c == nil || role == "" {
		return
	}
	bank := BankID(company.ID)
	modelID := agentPlaybookModelID(role)
	s.ensureModelOnce(bank, modelID, func() {
		s.createMentalModelIfMissing(ctx, bank, CreateMentalModelRequest{
			ID:   modelID,
			Name: fmt.Sprintf("Playbook: %s", role),
			SourceQuery: "What working approaches, recurring mistakes, and lessons has this agent accumulated " +
				"across its runs? Focus on actionable do's and don'ts.",
			Tags:                      []string{agentTag(role)},
			MaxTokens:                 agentPlaybookModelMaxTokens,
			RefreshAfterConsolidation: true,
		})
	})
}

// EnsureOpenBlockersModel creates (if missing) the bank-wide synthesis of
// unresolved blockers and recurring failures, useful for orchestration
// sessions that need to see systemic problems no single task reveals.
func (s *Service) EnsureOpenBlockersModel(ctx context.Context, company db.Company) {
	c := s.client()
	if c == nil {
		return
	}
	bank := BankID(company.ID)
	s.ensureModelOnce(bank, openBlockersModelID, func() {
		s.createMentalModelIfMissing(ctx, bank, CreateMentalModelRequest{
			ID:   openBlockersModelID,
			Name: "Open blockers",
			SourceQuery: "What unresolved blockers, failed attempts and recurring errors currently exist across " +
				"tasks? Ignore anything that a later run resolved.",
			MaxTokens:                 openBlockersModelMaxTokens,
			RefreshAfterConsolidation: true,
		})
	})
}

// createMentalModelIfMissing avoids duplicate-creation errors across process
// restarts (the in-process ensuredModels guard only covers one process
// lifetime): it first checks whether the model already exists server-side.
func (s *Service) createMentalModelIfMissing(ctx context.Context, bank string, req CreateMentalModelRequest) {
	c := s.client()
	if c == nil { // backend went away between the caller's check and now
		s.ensuredModels.Delete(bank + "/" + req.ID)
		return
	}
	if _, err := c.GetMentalModelRaw(ctx, bank, req.ID); err == nil {
		return // already exists
	}
	if _, err := c.CreateMentalModel(ctx, bank, req); err != nil {
		log.Printf("hindsight: create mental model %q on %s failed (non-fatal): %v", req.ID, bank, err)
		s.ensuredModels.Delete(bank + "/" + req.ID) // retry on next call
	}
}

// FetchModelContent fetches a mental model's synthesized content — a fast
// key-value lookup, cheap enough for every session. Returns ok=false when
// the model doesn't exist yet, hasn't finished its first generation, or the
// request fails; callers should simply skip that briefing section.
func (s *Service) FetchModelContent(ctx context.Context, companyID int32, modelID string) (string, bool) {
	c := s.client()
	if c == nil {
		return "", false
	}
	raw, err := c.GetMentalModelRaw(ctx, BankID(companyID), modelID)
	if err != nil {
		return "", false
	}
	var resp struct {
		Content *string `json:"content"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return "", false
	}
	if resp.Content == nil {
		return "", false
	}
	content := strings.TrimSpace(*resp.Content)
	if content == "" || strings.EqualFold(content, "Generating content...") {
		return "", false
	}
	return content, true
}

// ProjectStateModelID, AgentPlaybookModelID and OpenBlockersModelID are the
// deterministic ids used by callers that only need to fetch (not ensure) a
// model — e.g. the memory UI's Insights tab listing all of a company's
// models by id doesn't need these, but engine call sites fetching a specific
// model do.
func ProjectStateModelID(projectID int32) string { return projectStateModelID(projectID) }
func AgentPlaybookModelID(role string) string    { return agentPlaybookModelID(role) }
func OpenBlockersModelID() string                { return openBlockersModelID }
