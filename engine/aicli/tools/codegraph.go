package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"agent-orchestrator/engine/aicli"
)

const codegraphCmd = "codegraph"

// cgEnv holds the shared environment for all codegraph tools.
type cgEnv struct {
	workspacePath    string
	externalReposDir string
}

func newCGEnv(workspacePath string) *cgEnv {
	return &cgEnv{
		workspacePath:    workspacePath,
		externalReposDir: filepath.Join(os.TempDir(), "paperclip-codegraph"),
	}
}

// resolveRepoPath resolves a path for codegraph commands.
//   - Empty or "." → workspace root
//   - Relative → joined with workspace (must stay inside)
//   - Absolute → must be inside workspace or externalReposDir
func (e *cgEnv) resolveRepoPath(path string) (string, error) {
	ws := filepath.Clean(e.workspacePath)
	if path == "" || path == "." {
		return ws, nil
	}
	if !filepath.IsAbs(path) {
		abs := filepath.Clean(filepath.Join(e.workspacePath, path))
		if abs != ws && !strings.HasPrefix(abs, ws+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q escapes the workspace", path)
		}
		return abs, nil
	}
	clean := filepath.Clean(path)
	ext := filepath.Clean(e.externalReposDir)
	if clean == ws || strings.HasPrefix(clean, ws+string(filepath.Separator)) ||
		clean == ext || strings.HasPrefix(clean, ext+string(filepath.Separator)) {
		return clean, nil
	}
	return "", fmt.Errorf("path %q is outside the workspace and external repos directory", path)
}

// externalRepoPath returns a stable local directory path for a git URL.
func (e *cgEnv) externalRepoPath(repoURL string) string {
	h := sha256.Sum256([]byte(repoURL))
	return filepath.Join(e.externalReposDir, fmt.Sprintf("%x", h[:8]))
}

// runCG executes a codegraph command in dir with the given args.
func (e *cgEnv) runCG(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, error) {
	if _, err := exec.LookPath(codegraphCmd); err != nil {
		return "", fmt.Errorf("codegraph not found in PATH; install with: npm i -g @colbymchenry/codegraph")
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, codegraphCmd, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	result := strings.TrimSpace(string(out))
	if len(result) > 100_000 {
		result = result[:100_000] + "\n... (truncated)"
	}
	if err != nil {
		if result != "" {
			return fmt.Sprintf("codegraph error: %v\n%s", err, result), nil
		}
		return fmt.Sprintf("codegraph error: %v", err), nil
	}
	if result == "" {
		return "(no output)", nil
	}
	return result, nil
}

// ---- CodegraphInit ----

// CodegraphInit initializes or re-indexes a repository with codegraph.
// For external repos, it clones them first.
type CodegraphInit struct{ env *cgEnv }

func NewCodegraphInit(workspacePath string) *CodegraphInit {
	return &CodegraphInit{env: newCGEnv(workspacePath)}
}

func (t *CodegraphInit) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "codegraph_init",
			Description: "Initialize a codegraph knowledge graph for a repository, enabling semantic code search and analysis. " +
				"For the current workspace, call with no arguments. For a subdirectory, pass path. " +
				"For an external git repository, pass repo_url — it will be cloned locally and indexed. " +
				"Returns the local path of the indexed repository; pass this as repo_path to other codegraph tools.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "Subdirectory within the workspace to index (default: workspace root). Ignored when repo_url is set."
					},
					"repo_url": {
						"type": "string",
						"description": "Git URL of an external repository to clone and index (e.g. https://github.com/owner/repo)."
					},
					"force": {
						"type": "boolean",
						"description": "Force a full re-index of an already-initialized repository."
					}
				}
			}`),
		},
	}
}

func (t *CodegraphInit) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path    string `json:"path"`
		RepoURL string `json:"repo_url"`
		Force   bool   `json:"force"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}

	var targetPath string

	if p.RepoURL != "" {
		targetPath = t.env.externalRepoPath(p.RepoURL)
		if err := os.MkdirAll(t.env.externalReposDir, 0o755); err != nil {
			return "", fmt.Errorf("cannot create external repos directory: %w", err)
		}
		if _, err := os.Stat(filepath.Join(targetPath, ".git")); os.IsNotExist(err) {
			cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(cloneCtx, "git", "clone", p.RepoURL, targetPath)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return fmt.Sprintf("git clone failed: %v\n%s", err, string(out)), nil
			}
		} else {
			pullCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(pullCtx, "git", "pull")
			cmd.Dir = targetPath
			cmd.CombinedOutput() // pull failure is non-fatal; index proceeds
			_ = cancel
		}
	} else {
		var err error
		targetPath, err = t.env.resolveRepoPath(p.Path)
		if err != nil {
			return "", err
		}
	}

	cgArgs := []string{"init", targetPath}
	if p.Force {
		cgArgs = []string{"index", "--force", targetPath}
	}
	result, err := t.env.runCG(ctx, targetPath, 10*time.Minute, cgArgs...)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Indexed at: %s\n\n%s", targetPath, result), nil
}

// ---- CodegraphQuery ----

// CodegraphQuery searches for symbols in a codegraph-indexed repository.
type CodegraphQuery struct{ env *cgEnv }

func NewCodegraphQuery(workspacePath string) *CodegraphQuery {
	return &CodegraphQuery{env: newCGEnv(workspacePath)}
}

func (t *CodegraphQuery) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "codegraph_query",
			Description: "Search for symbols (functions, classes, methods, etc.) in a codegraph-indexed repository. Requires codegraph_init to have been run first.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"search": {
						"type": "string",
						"description": "Symbol name or pattern to search for"
					},
					"kind": {
						"type": "string",
						"description": "Filter by symbol kind: function, class, method, interface, struct, variable, constant, etc."
					},
					"limit": {
						"type": "integer",
						"description": "Maximum number of results (default 20)"
					},
					"repo_path": {
						"type": "string",
						"description": "Path to the indexed repository. Use the path returned by codegraph_init for external repos. Defaults to workspace root."
					}
				},
				"required": ["search"]
			}`),
		},
	}
}

func (t *CodegraphQuery) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Search   string `json:"search"`
		Kind     string `json:"kind"`
		Limit    int    `json:"limit"`
		RepoPath string `json:"repo_path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	dir, err := t.env.resolveRepoPath(p.RepoPath)
	if err != nil {
		return "", err
	}
	cgArgs := []string{"query", p.Search}
	if p.Kind != "" {
		cgArgs = append(cgArgs, "--kind", p.Kind)
	}
	if p.Limit > 0 {
		cgArgs = append(cgArgs, "--limit", fmt.Sprintf("%d", p.Limit))
	}
	return t.env.runCG(ctx, dir, 2*time.Minute, cgArgs...)
}

// ---- CodegraphExplore ----

// CodegraphExplore performs a semantic one-shot analysis of a repository.
type CodegraphExplore struct{ env *cgEnv }

func NewCodegraphExplore(workspacePath string) *CodegraphExplore {
	return &CodegraphExplore{env: newCGEnv(workspacePath)}
}

func (t *CodegraphExplore) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name: "codegraph_explore",
			Description: "Perform a one-shot semantic exploration of a codegraph-indexed repository. " +
				"Accepts natural language questions (\"how does authentication work?\") or symbol names. " +
				"Returns line-numbered source code, call paths, and blast-radius summaries. " +
				"Requires codegraph_init to have been run first.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {
						"type": "string",
						"description": "Natural language question or symbol names to explore (e.g. \"how does auth work\" or \"parseConfig renderScene\")"
					},
					"repo_path": {
						"type": "string",
						"description": "Path to the indexed repository. Use the path returned by codegraph_init for external repos. Defaults to workspace root."
					}
				},
				"required": ["query"]
			}`),
		},
	}
}

func (t *CodegraphExplore) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query    string `json:"query"`
		RepoPath string `json:"repo_path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	dir, err := t.env.resolveRepoPath(p.RepoPath)
	if err != nil {
		return "", err
	}
	return t.env.runCG(ctx, dir, 3*time.Minute, "explore", p.Query)
}

// ---- CodegraphCallers ----

// CodegraphCallers finds all callers of a symbol.
type CodegraphCallers struct{ env *cgEnv }

func NewCodegraphCallers(workspacePath string) *CodegraphCallers {
	return &CodegraphCallers{env: newCGEnv(workspacePath)}
}

func (t *CodegraphCallers) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "codegraph_callers",
			Description: "Find all callers of a symbol in a codegraph-indexed repository. Requires codegraph_init to have been run first.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"symbol": {
						"type": "string",
						"description": "Symbol name to find callers for (e.g. parseConfig, MyClass.myMethod)"
					},
					"limit": {
						"type": "integer",
						"description": "Maximum number of results (default 20)"
					},
					"repo_path": {
						"type": "string",
						"description": "Path to the indexed repository. Defaults to workspace root."
					}
				},
				"required": ["symbol"]
			}`),
		},
	}
}

func (t *CodegraphCallers) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Symbol   string `json:"symbol"`
		Limit    int    `json:"limit"`
		RepoPath string `json:"repo_path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	dir, err := t.env.resolveRepoPath(p.RepoPath)
	if err != nil {
		return "", err
	}
	cgArgs := []string{"callers", p.Symbol}
	if p.Limit > 0 {
		cgArgs = append(cgArgs, "--limit", fmt.Sprintf("%d", p.Limit))
	}
	return t.env.runCG(ctx, dir, 2*time.Minute, cgArgs...)
}

// ---- CodegraphCallees ----

// CodegraphCallees finds all functions called by a symbol.
type CodegraphCallees struct{ env *cgEnv }

func NewCodegraphCallees(workspacePath string) *CodegraphCallees {
	return &CodegraphCallees{env: newCGEnv(workspacePath)}
}

func (t *CodegraphCallees) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "codegraph_callees",
			Description: "Find all functions called by a symbol in a codegraph-indexed repository. Requires codegraph_init to have been run first.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"symbol": {
						"type": "string",
						"description": "Symbol name to find callees for"
					},
					"limit": {
						"type": "integer",
						"description": "Maximum number of results (default 20)"
					},
					"repo_path": {
						"type": "string",
						"description": "Path to the indexed repository. Defaults to workspace root."
					}
				},
				"required": ["symbol"]
			}`),
		},
	}
}

func (t *CodegraphCallees) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Symbol   string `json:"symbol"`
		Limit    int    `json:"limit"`
		RepoPath string `json:"repo_path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	dir, err := t.env.resolveRepoPath(p.RepoPath)
	if err != nil {
		return "", err
	}
	cgArgs := []string{"callees", p.Symbol}
	if p.Limit > 0 {
		cgArgs = append(cgArgs, "--limit", fmt.Sprintf("%d", p.Limit))
	}
	return t.env.runCG(ctx, dir, 2*time.Minute, cgArgs...)
}

// ---- CodegraphImpact ----

// CodegraphImpact analyzes the blast radius of changing a symbol.
type CodegraphImpact struct{ env *cgEnv }

func NewCodegraphImpact(workspacePath string) *CodegraphImpact {
	return &CodegraphImpact{env: newCGEnv(workspacePath)}
}

func (t *CodegraphImpact) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "codegraph_impact",
			Description: "Analyze the blast radius of changing a symbol — shows what code would be affected if this symbol is modified. Requires codegraph_init.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"symbol": {
						"type": "string",
						"description": "Symbol name to analyze impact for"
					},
					"depth": {
						"type": "integer",
						"description": "How many levels of dependency to follow (default 2)"
					},
					"repo_path": {
						"type": "string",
						"description": "Path to the indexed repository. Defaults to workspace root."
					}
				},
				"required": ["symbol"]
			}`),
		},
	}
}

func (t *CodegraphImpact) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Symbol   string `json:"symbol"`
		Depth    int    `json:"depth"`
		RepoPath string `json:"repo_path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	dir, err := t.env.resolveRepoPath(p.RepoPath)
	if err != nil {
		return "", err
	}
	cgArgs := []string{"impact", p.Symbol}
	if p.Depth > 0 {
		cgArgs = append(cgArgs, "--depth", fmt.Sprintf("%d", p.Depth))
	}
	return t.env.runCG(ctx, dir, 2*time.Minute, cgArgs...)
}

// ---- CodegraphNode ----

// CodegraphNode displays the source of a symbol and its callers.
type CodegraphNode struct{ env *cgEnv }

func NewCodegraphNode(workspacePath string) *CodegraphNode {
	return &CodegraphNode{env: newCGEnv(workspacePath)}
}

func (t *CodegraphNode) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "codegraph_node",
			Description: "Display the source code of a symbol or file, along with its call paths and callers. Requires codegraph_init.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"symbol": {
						"type": "string",
						"description": "Symbol name or file path to inspect"
					},
					"repo_path": {
						"type": "string",
						"description": "Path to the indexed repository. Defaults to workspace root."
					}
				},
				"required": ["symbol"]
			}`),
		},
	}
}

func (t *CodegraphNode) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Symbol   string `json:"symbol"`
		RepoPath string `json:"repo_path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	dir, err := t.env.resolveRepoPath(p.RepoPath)
	if err != nil {
		return "", err
	}
	return t.env.runCG(ctx, dir, 2*time.Minute, "node", p.Symbol)
}

// ---- CodegraphFiles ----

// CodegraphFiles shows the file structure of an indexed repository.
type CodegraphFiles struct{ env *cgEnv }

func NewCodegraphFiles(workspacePath string) *CodegraphFiles {
	return &CodegraphFiles{env: newCGEnv(workspacePath)}
}

func (t *CodegraphFiles) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "codegraph_files",
			Description: "Show the file structure of a codegraph-indexed repository with indexing statistics. Requires codegraph_init.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"path": {
						"type": "string",
						"description": "Subdirectory within the repository to list (optional)"
					},
					"max_depth": {
						"type": "integer",
						"description": "Maximum directory depth to display"
					},
					"repo_path": {
						"type": "string",
						"description": "Path to the indexed repository. Defaults to workspace root."
					}
				}
			}`),
		},
	}
}

func (t *CodegraphFiles) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path     string `json:"path"`
		MaxDepth int    `json:"max_depth"`
		RepoPath string `json:"repo_path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	dir, err := t.env.resolveRepoPath(p.RepoPath)
	if err != nil {
		return "", err
	}
	cgArgs := []string{"files"}
	if p.Path != "" {
		cgArgs = append(cgArgs, p.Path)
	}
	if p.MaxDepth > 0 {
		cgArgs = append(cgArgs, "--max-depth", fmt.Sprintf("%d", p.MaxDepth))
	}
	return t.env.runCG(ctx, dir, 2*time.Minute, cgArgs...)
}
