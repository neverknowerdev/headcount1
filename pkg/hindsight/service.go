package hindsight

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agent-orchestrator/db"
)

// Service layers this app's memory conventions on top of the Hindsight API:
//
//   - one bank per project ("proj-<id>") holding the project's documentation
//     (.md files only — code exploration is CodeGraph's job); each file is a
//     Hindsight document keyed "doc:<relative path>", so re-retaining a
//     changed file upserts and deleting a file removes its memories;
//   - one shared bank per company ("runs-<companyID>") holding agent run
//     experience, scoped with tags: "agent:<role>", "session:<rootRunID>"
//     (one conversation tree), "task:<refKey>", "project:<id>". A shared
//     bank with tag scoping is the Hindsight-recommended layout when agents
//     must see each other's experience (e.g. CMO recalling what CTO did),
//     while tags still give per-agent / per-conversation levels.
type Service struct {
	client  func() *Client // nil result means hindsight is unavailable
	q       *db.Queries
	timeout time.Duration
}

func NewService(q *db.Queries, client func() *Client) *Service {
	return &Service{client: client, q: q, timeout: 120 * time.Second}
}

// Available reports whether the Hindsight backend is reachable right now.
func (s *Service) Available() bool {
	return s != nil && s.client() != nil
}

func ProjectBankID(projectID int32) string { return fmt.Sprintf("proj-%d", projectID) }
func CompanyBankID(companyID int32) string { return fmt.Sprintf("runs-%d", companyID) }
func agentTag(role string) string {
	return "agent:" + strings.ToLower(strings.ReplaceAll(role, " ", "-"))
}
func DocDocumentID(relPath string) string { return "doc:" + relPath }
func runDocumentID(runID int32) string    { return fmt.Sprintf("run-%d", runID) }

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
// the project's memory bank and removes memories of deleted files. It is the
// single code path for both initial ingestion (project added) and updates
// (doc files changed), relying on Hindsight's document_id upsert semantics.
func (s *Service) SyncProjectDocs(ctx context.Context, project db.Project, repoPath string) (added, updated, removed int, err error) {
	c := s.client()
	if c == nil {
		return 0, 0, 0, fmt.Errorf("hindsight not available")
	}
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

	bank := ProjectBankID(project.ID)
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
		item := MemoryItem{
			Content:    string(content),
			Timestamp:  "unset", // reference documentation is timeless
			Context:    fmt.Sprintf("Documentation file %q of project %q", rel, project.Name),
			DocumentID: DocDocumentID(rel),
			Tags:       []string{fmt.Sprintf("project:%d", project.ID), "source:docs"},
			Metadata:   map[string]string{"path": rel, "project": project.Name},
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
			DocumentID: DocDocumentID(rel),
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

	tags := []string{agentTag(role)}
	if run.RootRunID != nil {
		tags = append(tags, fmt.Sprintf("session:%d", *run.RootRunID))
	}
	if task.RefKey != "" {
		tags = append(tags, "task:"+strings.ToLower(task.RefKey))
	}
	if task.ProjectID != nil {
		tags = append(tags, fmt.Sprintf("project:%d", *task.ProjectID))
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
	}
	return c.Retain(ctx, CompanyBankID(company.ID), []MemoryItem{item}, true)
}

// Recall queries the company's experience bank and, when projectID is set,
// the project's documentation bank, merging the results. agentRole optionally
// narrows experience results to one agent's memories.
func (s *Service) Recall(ctx context.Context, companyID int32, projectID *int32, agentRole, query string, maxTokens int) ([]RecallResult, error) {
	c := s.client()
	if c == nil {
		return nil, fmt.Errorf("hindsight not available")
	}
	if maxTokens <= 0 {
		maxTokens = 2048
	}
	req := RecallRequest{Query: query, Budget: "mid", MaxTokens: maxTokens}
	if agentRole != "" {
		req.Tags = []string{agentTag(agentRole)}
		req.TagsMatch = "any_strict"
	}
	var results []RecallResult
	runsResp, err := c.Recall(ctx, CompanyBankID(companyID), req)
	if err != nil {
		return nil, err
	}
	results = append(results, runsResp.Results...)

	if projectID != nil {
		docResp, derr := c.Recall(ctx, ProjectBankID(*projectID), RecallRequest{
			Query: query, Budget: "mid", MaxTokens: maxTokens,
		})
		if derr != nil {
			log.Printf("hindsight: project bank recall failed: %v", derr)
		} else {
			results = append(results, docResp.Results...)
		}
	}
	return results, nil
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
		fmt.Fprintf(&b, "- %s", line)
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
// memory is unavailable — callers just skip the section.
func (s *Service) TaskBriefing(ctx context.Context, companyID int32, projectID *int32, task db.Task) string {
	if !s.Available() {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	query := task.Title
	if task.Description != "" {
		query += "\n" + firstN(task.Description, 800)
	}
	results, err := s.Recall(ctx, companyID, projectID, "", query, 2048)
	if err != nil {
		log.Printf("hindsight: task briefing recall failed: %v", err)
		return ""
	}
	if len(results) == 0 {
		return ""
	}
	return FormatResults(results)
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
