package hindsight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"agent-orchestrator/db"
)

// Default recall token budgets, per the Hindsight-recommended cost/quality
// tiers. The tool budget is higher because it is an explicit, occasional
// agent action; the briefing budget is lower because it lands in every
// session's system prompt.
const (
	defaultRecallMaxTokens   = 6144
	defaultBriefingMaxTokens = 4096
)

// maxDocFileBytes caps how large a documentation file may be before it is
// skipped by SyncProjectDocs — every retained byte is LLM-processed by
// Hindsight, so a stray generated/vendored .md must not burn the budget.
const maxDocFileBytes = 1 << 20 // 1 MiB

// recallTypes always includes observations — Hindsight's deduplicated,
// consolidated knowledge — alongside raw facts, per recall recommendation:
// prefer observations over the raw facts they were built from.
var recallTypes = []string{"world", "experience", "observation"}

// Service layers this app's memory conventions on top of the Hindsight API.
//
// A single bank per company ("company-<id>") holds everything: project
// documentation and agent run experience together. Hindsight banks are
// fully isolated (no cross-bank entity resolution, graph traversal, or rank
// fusion), so splitting docs and experience into separate banks would sever
// the entity graph between "the ICP backend" mentioned in docs and the CTO's
// run about implementing it — a single bank with tag scoping is Hindsight's
// recommended layout unless hard isolation is required (it is not, here).
//
// Tags carry every level of scoping within the shared bank:
//   - "project:<id>", "source:docs"                — doc memories
//   - "agent:<role>", "session:<rootRunID>", "task:<refKey>", "project:<id>" — run memories
//
// Documents: each doc file is "doc:<projectID>/<relative path>" (the project
// prefix keeps paths unique across projects sharing one bank); each run is
// "run-<runID>". Hindsight's document_id upsert semantics mean re-retaining
// a changed file or deleting a stale one just works.
type Service struct {
	client  func() *Client // nil result means hindsight is unavailable
	q       *db.Queries
	timeout time.Duration

	// ensuredBanks guards EnsureBank so the config PATCH only runs once per
	// company per process lifetime, not on every retain/recall.
	ensuredBanks sync.Map // companyID -> struct{}
	// ensuredModels guards mental-model creation the same way.
	ensuredModels sync.Map // "bank/modelID" -> struct{}
	// docSyncs serializes SyncProjectDocs per project, so a startup sync and
	// a repo-update sync of the same project never interleave.
	docSyncs sync.Map // projectID -> *sync.Mutex
}

func NewService(q *db.Queries, client func() *Client) *Service {
	return &Service{client: client, q: q, timeout: 120 * time.Second}
}

// EnsureBank idempotently configures a company's memory bank with a mission,
// disposition and directives suited to being the collective long-term memory
// of an AI agent team, so reflect (and later, mental model synthesis) reason
// with the right personality instead of Hindsight's generic defaults.
// Cheap to call from every retain/recall path: guarded in-process, and the
// underlying PATCH is idempotent even if called again after a restart.
func (s *Service) EnsureBank(ctx context.Context, company db.Company) {
	if _, done := s.ensuredBanks.LoadOrStore(company.ID, struct{}{}); done {
		return
	}
	c := s.client()
	if c == nil {
		s.ensuredBanks.Delete(company.ID) // retry next call once available
		return
	}
	bank := BankID(company.ID)
	if err := c.CreateBank(ctx, bank); err != nil {
		log.Printf("hindsight: create bank %s failed (will retry next call): %v", bank, err)
		s.ensuredBanks.Delete(company.ID)
		return
	}
	updates := map[string]interface{}{
		"reflect_mission": fmt.Sprintf(
			"I am the collective long-term memory of %s's AI agent team. I track project "+
				"documentation, implementation state, task outcomes and mistakes so agents don't repeat them.",
			company.Name),
		"disposition_skepticism": 4, // agents' self-reported successes deserve doubt
		"disposition_literalism": 3,
		"disposition_empathy":    1,
	}
	if err := c.UpdateBankConfig(ctx, bank, updates); err != nil {
		log.Printf("hindsight: bank config for %s failed (will retry next call): %v", bank, err)
		s.ensuredBanks.Delete(company.ID)
		return
	}
	directives := []struct{ name, content string }{
		{"cite-source", "Always state which task or run a claim comes from."},
		{"no-false-completion", "Never present a blocked or failed attempt as a completed implementation."},
	}
	existing := map[string]bool{}
	if raw, lerr := c.ListDirectivesRaw(ctx, bank); lerr == nil {
		var resp struct {
			Items []struct {
				Name string `json:"name"`
			} `json:"items"`
		}
		if jerr := json.Unmarshal(raw, &resp); jerr == nil {
			for _, it := range resp.Items {
				existing[it.Name] = true
			}
		}
	}
	for _, d := range directives {
		if existing[d.name] {
			continue
		}
		if err := c.CreateDirective(ctx, bank, d.name, d.content); err != nil {
			log.Printf("hindsight: create directive %q on %s failed (non-fatal): %v", d.name, bank, err)
		}
	}
}

// ResetEnsured clears the per-process "already ensured" guards for bank
// config and mental models, so the next retain/recall re-applies them. Needed
// when bank identity is reused for different data — e.g. the e2e DB wipe
// resets company IDs while this process keeps running.
func (s *Service) ResetEnsured() {
	s.ensuredBanks.Clear()
	s.ensuredModels.Clear()
}

// Available reports whether the Hindsight backend is reachable right now.
func (s *Service) Available() bool {
	return s != nil && s.client() != nil
}

// BankID returns the single memory bank for a company.
func BankID(companyID int32) string { return fmt.Sprintf("company-%d", companyID) }

func agentTag(role string) string {
	return "agent:" + slugify(role)
}

// ProjectTag scopes a memory to one project. Keyed by the project name's
// slug (not the numeric id) so tags read naturally in the Memory UI —
// "project:gm-coin" instead of "project:3". Exported for the UI/e2e to
// derive the same tag from a project name.
func ProjectTag(projectName string) string {
	return "project:" + slugify(projectName)
}

// slugify lowercases and collapses any non-alphanumeric run into a single
// hyphen ("GM Coin" → "gm-coin"). Deterministic so tags stay stable.
func slugify(s string) string {
	var b strings.Builder
	lastHyphen := true // suppress a leading hyphen
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastHyphen = false
		default:
			if !lastHyphen {
				b.WriteByte('-')
				lastHyphen = true
			}
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}
func DocDocumentID(projectID int32, relPath string) string {
	return fmt.Sprintf("doc:%d/%s", projectID, relPath)
}
func runDocumentID(runID int32) string { return fmt.Sprintf("run-%d", runID) }

// docFile reports whether a path is a documentation file we feed to memory.
func docFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".mdx", ".markdown":
		return true
	}
	return false
}

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "dist": true,
	"build": true, ".venv": true, "venv": true, "__pycache__": true,
}

// SyncProjectDocs walks the project repo, feeds new/changed .md files into
// the company's shared memory bank and removes memories of deleted files. It
// is the single code path for both initial ingestion (project added) and
// updates (doc files changed), relying on Hindsight's document_id upsert
// semantics.
func (s *Service) SyncProjectDocs(ctx context.Context, company db.Company, project db.Project, repoPath string) (added, updated, removed int, err error) {
	c := s.client()
	if c == nil {
		return 0, 0, 0, fmt.Errorf("hindsight not available")
	}
	// One sync per project at a time: a startup sync racing a repo-update
	// sync would double-retain and fight over the tracking rows.
	muAny, _ := s.docSyncs.LoadOrStore(project.ID, &sync.Mutex{})
	mu := muAny.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()

	s.EnsureBank(ctx, company)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	known, err := s.q.ListHindsightDocsByProject(ctx, project.ID)
	if err != nil {
		return 0, 0, 0, err
	}
	knownByPath := make(map[string]db.HindsightDocument, len(known))
	for _, d := range known {
		knownByPath[d.Path] = d
	}

	bank := BankID(company.ID)
	seen := map[string]bool{}
	var paths []string
	walkErr := filepath.WalkDir(repoPath, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return nil // unreadable entries are skipped, not fatal
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if docFile(path) {
			paths = append(paths, path)
		}
		return nil
	})
	if walkErr != nil {
		return 0, 0, 0, walkErr
	}
	sort.Strings(paths)

	for _, path := range paths {
		rel, rerr := filepath.Rel(repoPath, path)
		if rerr != nil {
			continue
		}
		if info, ierr := os.Lstat(path); ierr != nil || !info.Mode().IsRegular() || info.Size() > maxDocFileBytes {
			if ierr == nil && info.Size() > maxDocFileBytes {
				log.Printf("hindsight: skipping %s (%d bytes > %d limit)", path, info.Size(), maxDocFileBytes)
			}
			continue
		}
		content, rerr := os.ReadFile(path)
		if rerr != nil || len(content) == 0 {
			continue
		}
		sum := sha256.Sum256(content)
		hash := hex.EncodeToString(sum[:])
		seen[rel] = true

		prev, existed := knownByPath[rel]
		if existed && prev.SHA256 == hash {
			continue // unchanged
		}
		projectTag := ProjectTag(project.Name)
		item := MemoryItem{
			Content:    string(content),
			Timestamp:  "unset", // reference documentation is timeless
			Context:    fmt.Sprintf("Documentation file %q of project %q", rel, project.Name),
			DocumentID: DocDocumentID(project.ID, rel),
			Tags:       []string{projectTag, "source:docs"},
			Metadata:   map[string]string{"path": rel, "project": project.Name},
			// Consolidate observations at the project level so a project's
			// docs and run experience synthesize together, independent of
			// other projects sharing this bank.
			ObservationScopes: [][]string{{projectTag}},
		}
		if rerr := c.Retain(ctx, bank, []MemoryItem{item}, false); rerr != nil {
			log.Printf("hindsight: retain doc %s failed: %v", rel, rerr)
			continue
		}
		if existed {
			updated++
		} else {
			added++
		}
		_ = s.q.UpsertHindsightDoc(ctx, db.HindsightDocument{
			ProjectID:  project.ID,
			Path:       rel,
			DocumentID: DocDocumentID(project.ID, rel),
			SHA256:     hash,
		})
	}

	// Files tracked before but gone from disk: drop their memories.
	for _, d := range known {
		if seen[d.Path] {
			continue
		}
		if derr := c.DeleteDocument(ctx, bank, d.DocumentID); derr != nil {
			log.Printf("hindsight: delete doc %s failed: %v", d.Path, derr)
			continue
		}
		_ = s.q.DeleteHindsightDoc(ctx, project.ID, d.Path)
		removed++
	}
	return added, updated, removed, nil
}

// RetainRunOutcome feeds one finished agent session into the company's
// experience bank: what was attempted, how it ended, and any error. Each run
// is its own document; tags carry the per-agent and per-conversation levels.
func (s *Service) RetainRunOutcome(ctx context.Context, company db.Company, task db.Task, run db.Run, status, errMsg string) error {
	c := s.client()
	if c == nil {
		return nil
	}
	role := run.AgentConfigName
	if role == "" {
		role = "agent"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Agent %s worked on task %s (%q) and the run ended with status %q, task status %q.\n",
		role, task.RefKey, task.Title, status, task.Status)
	if task.Description != "" {
		fmt.Fprintf(&b, "Task description: %s\n", firstN(task.Description, 1500))
	}
	if run.ResultDescription != "" {
		fmt.Fprintf(&b, "Result: %s\n", run.ResultDescription)
	}
	if run.ResultExplanation != "" {
		fmt.Fprintf(&b, "Details: %s\n", firstN(run.ResultExplanation, 4000))
	}
	if errMsg != "" {
		fmt.Fprintf(&b, "Error encountered: %s\n", firstN(errMsg, 2000))
	}

	agentTagValue := agentTag(role)
	tags := []string{agentTagValue}
	if run.RootRunID != nil {
		tags = append(tags, fmt.Sprintf("session:%d", *run.RootRunID))
	}
	if task.RefKey != "" {
		tags = append(tags, "task:"+strings.ToLower(task.RefKey))
	}
	scopes := [][]string{{agentTagValue}}
	if task.ProjectID != nil {
		projectName := ""
		if task.Project != nil && task.Project.Name != "" {
			projectName = task.Project.Name
		} else if p, perr := s.q.GetProject(ctx, *task.ProjectID); perr == nil {
			projectName = p.Name
		}
		if projectName != "" {
			projectTag := ProjectTag(projectName)
			tags = append(tags, projectTag)
			scopes = append(scopes, []string{projectTag})
		}
	}
	item := MemoryItem{
		Content:    b.String(),
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		Context:    fmt.Sprintf("Run %s of agent %s on task %s", run.Name, role, task.RefKey),
		DocumentID: runDocumentID(run.ID),
		Tags:       tags,
		Metadata: map[string]string{
			"run_id": fmt.Sprintf("%d", run.ID),
			"task":   task.RefKey,
			"agent":  role,
			"status": status,
		},
		// Consolidate per-agent (the "playbook" scope) and per-project (the
		// "project state" scope) so both future mental models have
		// deduplicated, up-to-date source material.
		ObservationScopes: scopes,
	}
	s.EnsureBank(ctx, company)
	return c.Retain(ctx, BankID(company.ID), []MemoryItem{item}, true)
}

// Recall queries the company's shared memory bank — project documentation
// and agent run experience together, so results are ranked by one fused
// pass instead of concatenating two independent searches. projectID is
// accepted for call-site compatibility (a future narrowing via tag_groups
// could use it) but does not currently filter; doc and run memories are
// both tagged "project:<id>" and rank fusion naturally favors relevant
// project content. agentRole optionally narrows to one agent's experience.
func (s *Service) Recall(ctx context.Context, companyID int32, projectID *int32, agentRole, query string, maxTokens int) ([]RecallResult, error) {
	c := s.client()
	if c == nil {
		return nil, fmt.Errorf("hindsight not available")
	}
	if maxTokens <= 0 {
		maxTokens = defaultRecallMaxTokens
	}
	req := RecallRequest{
		Query: query, Budget: "mid", MaxTokens: maxTokens,
		Types: recallTypes, PreferObservations: true,
	}
	if agentRole != "" {
		req.Tags = []string{agentTag(agentRole)}
		req.TagsMatch = "any_strict"
	}
	resp, err := c.Recall(ctx, BankID(companyID), req)
	if err != nil {
		return nil, err
	}
	return resp.Results, nil
}

// FormatResults renders recall results as a compact markdown list for
// injection into prompts and tool outputs.
func FormatResults(results []RecallResult) string {
	if len(results) == 0 {
		return "No relevant memories found."
	}
	var b strings.Builder
	for _, r := range results {
		line := strings.TrimSpace(r.Text)
		if line == "" {
			continue
		}
		// Observations are Hindsight's consolidated, deduplicated knowledge —
		// flag them so agents weight them above one-off raw facts.
		if r.Type == "observation" {
			fmt.Fprintf(&b, "- [insight] %s", line)
		} else {
			fmt.Fprintf(&b, "- %s", line)
		}
		var meta []string
		if r.Type != "" {
			meta = append(meta, r.Type)
		}
		if r.OccurredAt != "" {
			meta = append(meta, r.OccurredAt)
		}
		if r.DocumentID != "" {
			meta = append(meta, r.DocumentID)
		}
		if len(meta) > 0 {
			fmt.Fprintf(&b, " _(%s)_", strings.Join(meta, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// TaskBriefing retrieves memories relevant to a task before execution starts
// (the pre-task refinement step). Returns "" when nothing useful was found or
// memory is unavailable — callers just skip the section. maxTokens <= 0 uses
// the built-in briefing default.
func (s *Service) TaskBriefing(ctx context.Context, companyID int32, projectID *int32, task db.Task, maxTokens int) string {
	if !s.Available() {
		return ""
	}
	if maxTokens <= 0 {
		maxTokens = defaultBriefingMaxTokens
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	query := task.Title
	if task.Description != "" {
		query += "\n" + firstN(task.Description, 800)
	}
	results, err := s.Recall(ctx, companyID, projectID, "", query, maxTokens)
	if err != nil {
		log.Printf("hindsight: task briefing recall failed: %v", err)
		return ""
	}
	if len(results) == 0 {
		return ""
	}
	return FormatResults(results)
}

// firstN truncates s to at most n bytes without splitting a UTF-8 rune.
func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
