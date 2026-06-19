package aicli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Tool is a callable function that the agent can invoke during its loop.
type Tool struct {
	Def     ToolDef
	Execute func(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry holds all tools available to the agent and dispatches executions.
type Registry struct {
	tools map[string]*Tool
}

// NewRegistry creates an empty tool registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]*Tool)}
}

// Register adds a tool to the registry.
func (r *Registry) Register(t Tool) {
	r.tools[t.Def.Function.Name] = &t
}

// Defs returns the ToolDef slice for inclusion in a ChatRequest.
func (r *Registry) Defs() []ToolDef {
	defs := make([]ToolDef, 0, len(r.tools))
	for _, t := range r.tools {
		defs = append(defs, t.Def)
	}
	return defs
}

// Execute runs the named tool with the given JSON arguments.
func (r *Registry) Execute(ctx context.Context, name string, args json.RawMessage) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return t.Execute(ctx, args)
}

// DefaultTools returns a Registry with the built-in file/shell/web tools pre-loaded.
// All file tools are sandboxed to workspacePath — paths outside it are rejected.
func DefaultTools(workspacePath string) *Registry {
	r := NewRegistry()
	r.Register(readFileTool(workspacePath))
	r.Register(writeFileTool(workspacePath))
	r.Register(listDirTool(workspacePath))
	r.Register(execCommandTool(workspacePath))
	r.Register(grepTool(workspacePath))
	r.Register(webFetchTool())
	return r
}

// ---- read_file ---------------------------------------------------------------

func readFileTool(workspacePath string) Tool {
	return Tool{
		Def: ToolDef{
			Type: "function",
			Function: FuncMeta{
				Name:        "read_file",
				Description: "Read a file inside the workspace. Returns the file content as text.",
				Parameters: json.RawMessage(`{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"Path relative to the workspace root (e.g. \"src/main.go\")"}
					},
					"required":["path"]
				}`),
			},
		},
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path string `json:"path"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			resolved, err := resolvePath(workspacePath, p.Path)
			if err != nil {
				return "", err
			}
			data, err := os.ReadFile(resolved)
			if err != nil {
				return "", fmt.Errorf("read_file: %w", err)
			}
			content := string(data)
			if len(content) > 100_000 {
				content = content[:100_000] + "\n... (truncated)"
			}
			return content, nil
		},
	}
}

// ---- list_dir ----------------------------------------------------------------

func listDirTool(workspacePath string) Tool {
	return Tool{
		Def: ToolDef{
			Type: "function",
			Function: FuncMeta{
				Name:        "list_dir",
				Description: "List files inside the workspace. Returns a line-per-entry listing.",
				Parameters: json.RawMessage(`{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"Directory path relative to the workspace root (use \".\" for the root)"},
						"recursive":{"type":"boolean","description":"Whether to list recursively (default false)"}
					},
					"required":["path"]
				}`),
			},
		},
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path      string `json:"path"`
				Recursive bool   `json:"recursive"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			resolved, err := resolvePath(workspacePath, p.Path)
			if err != nil {
				return "", err
			}

			var sb strings.Builder
			if p.Recursive {
				err := filepath.WalkDir(resolved, func(path string, d os.DirEntry, err error) error {
					if err != nil {
						return nil // skip inaccessible entries
					}
					rel, _ := filepath.Rel(resolved, path)
					if d.IsDir() {
						sb.WriteString(rel + "/\n")
					} else {
						sb.WriteString(rel + "\n")
					}
					return nil
				})
				if err != nil {
					return "", fmt.Errorf("list_dir: %w", err)
				}
			} else {
				entries, err := os.ReadDir(resolved)
				if err != nil {
					return "", fmt.Errorf("list_dir: %w", err)
				}
				for _, e := range entries {
					if e.IsDir() {
						sb.WriteString(e.Name() + "/\n")
					} else {
						sb.WriteString(e.Name() + "\n")
					}
				}
			}
			if sb.Len() == 0 {
				return "(empty directory)", nil
			}
			return sb.String(), nil
		},
	}
}

// ---- exec_command ------------------------------------------------------------

func execCommandTool(workspacePath string) Tool {
	return Tool{
		Def: ToolDef{
			Type: "function",
			Function: FuncMeta{
				Name:        "exec_command",
				Description: "Execute a shell command inside the workspace. Runs with the workspace as the working directory. Only use relative paths — absolute paths outside the workspace are rejected.",
				Parameters: json.RawMessage(`{
					"type":"object",
					"properties":{
						"command":{"type":"string","description":"Shell command to run using relative paths (e.g. \"go test ./...\", \"ls src/\")"}
					},
					"required":["command"]
				}`),
			},
		},
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			if err := validateCommandPaths(workspacePath, p.Command); err != nil {
				return "", err
			}
			// 60-second hard cap so a misbehaving command can't stall the run forever.
			cmdCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()

			cmd := exec.CommandContext(cmdCtx, "sh", "-c", p.Command)
			cmd.Dir = workspacePath
			output, err := cmd.CombinedOutput()
			result := string(output)
			if len(result) > 50_000 {
				result = result[:50_000] + "\n... (truncated)"
			}
			if err != nil {
				return fmt.Sprintf("exit error: %v\n%s", err, result), nil
			}
			return result, nil
		},
	}
}

// validateCommandPaths rejects shell commands that reference absolute paths
// outside the workspace. It scans whitespace-separated tokens for absolute
// paths (starting with /) and checks each against the workspace boundary.
// This handles common cases like "ls /etc" or "cat /etc/passwd"; it does
// not prevent every possible bypass but keeps the agent within the workspace
// for straightforward file-navigation commands.
func validateCommandPaths(workspacePath, command string) error {
	workspace := filepath.Clean(workspacePath)
	for _, token := range strings.Fields(command) {
		token = strings.Trim(token, `"'`)
		if !filepath.IsAbs(token) {
			continue
		}
		clean := filepath.Clean(token)
		if clean != workspace && !strings.HasPrefix(clean, workspace+string(filepath.Separator)) {
			return fmt.Errorf("path %q escapes the workspace", token)
		}
	}
	return nil
}

// ---- grep --------------------------------------------------------------------

func grepTool(workspacePath string) Tool {
	return Tool{
		Def: ToolDef{
			Type: "function",
			Function: FuncMeta{
				Name:        "grep",
				Description: "Search for a regex pattern in workspace files. Returns matching lines with file:line format.",
				Parameters: json.RawMessage(`{
					"type":"object",
					"properties":{
						"pattern":{"type":"string","description":"Regular expression pattern to search for"},
						"path":{"type":"string","description":"File or directory relative to the workspace root to search (default: entire workspace)"},
						"recursive":{"type":"boolean","description":"Search recursively in directories (default true)"}
					},
					"required":["pattern"]
				}`),
			},
		},
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Pattern   string `json:"pattern"`
				Path      string `json:"path"`
				Recursive *bool  `json:"recursive"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}

			re, err := regexp.Compile(p.Pattern)
			if err != nil {
				return "", fmt.Errorf("invalid pattern: %w", err)
			}

			searchPath := workspacePath
			if p.Path != "" {
				sp, err := resolvePath(workspacePath, p.Path)
				if err != nil {
					return "", err
				}
				searchPath = sp
			}

			recursive := true
			if p.Recursive != nil {
				recursive = *p.Recursive
			}

			var sb strings.Builder
			matchCount := 0
			const maxMatches = 500

			search := func(filePath string) error {
				if matchCount >= maxMatches {
					return nil
				}
				data, err := os.ReadFile(filePath)
				if err != nil {
					return nil // skip unreadable files
				}
				lines := strings.Split(string(data), "\n")
				for i, line := range lines {
					if re.MatchString(line) {
						rel, _ := filepath.Rel(workspacePath, filePath)
						sb.WriteString(fmt.Sprintf("%s:%d: %s\n", rel, i+1, line))
						matchCount++
						if matchCount >= maxMatches {
							sb.WriteString("... (max matches reached)\n")
							break
						}
					}
				}
				return nil
			}

			info, err := os.Stat(searchPath)
			if err != nil {
				return "", fmt.Errorf("grep: path not found: %s", searchPath)
			}

			if info.IsDir() && recursive {
				filepath.WalkDir(searchPath, func(path string, d os.DirEntry, err error) error {
					if err != nil || d.IsDir() {
						return nil
					}
					return search(path)
				})
			} else if info.IsDir() {
				entries, _ := os.ReadDir(searchPath)
				for _, e := range entries {
					if !e.IsDir() {
						search(filepath.Join(searchPath, e.Name()))
					}
				}
			} else {
				search(searchPath)
			}

			if sb.Len() == 0 {
				return "(no matches)", nil
			}
			return sb.String(), nil
		},
	}
}

// ---- web_fetch ---------------------------------------------------------------

func webFetchTool() Tool {
	return Tool{
		Def: ToolDef{
			Type: "function",
			Function: FuncMeta{
				Name:        "web_fetch",
				Description: "Fetch the content of a URL. Returns the response body as text (truncated to 50KB).",
				Parameters: json.RawMessage(`{
					"type":"object",
					"properties":{
						"url":{"type":"string","description":"HTTP or HTTPS URL to fetch"}
					},
					"required":["url"]
				}`),
			},
		},
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}

			reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()

			req, err := http.NewRequestWithContext(reqCtx, "GET", p.URL, nil)
			if err != nil {
				return "", fmt.Errorf("web_fetch: invalid URL: %w", err)
			}
			req.Header.Set("User-Agent", "paperclip-agent/1.0")

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return "", fmt.Errorf("web_fetch: %w", err)
			}
			defer resp.Body.Close()

			body, err := io.ReadAll(io.LimitReader(resp.Body, 50_000))
			if err != nil {
				return "", fmt.Errorf("web_fetch: read body: %w", err)
			}
			return fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, string(body)), nil
		},
	}
}

// ---- write_file -------------------------------------------------------------

func writeFileTool(workspacePath string) Tool {
	return Tool{
		Def: ToolDef{
			Type: "function",
			Function: FuncMeta{
				Name:        "write_file",
				Description: "Write content to a file inside the workspace. Creates parent directories as needed.",
				Parameters: json.RawMessage(`{
					"type":"object",
					"properties":{
						"path":{"type":"string","description":"Path relative to the workspace root (e.g. \"output/result.txt\")"},
						"content":{"type":"string","description":"Content to write to the file"}
					},
					"required":["path","content"]
				}`),
			},
		},
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Path    string `json:"path"`
				Content string `json:"content"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			resolved, err := resolvePath(workspacePath, p.Path)
			if err != nil {
				return "", err
			}
			if err := os.MkdirAll(filepath.Dir(resolved), 0755); err != nil {
				return "", fmt.Errorf("write_file: mkdir: %w", err)
			}
			if err := os.WriteFile(resolved, []byte(p.Content), 0644); err != nil {
				return "", fmt.Errorf("write_file: %w", err)
			}
			return fmt.Sprintf("wrote %d bytes to %s", len(p.Content), p.Path), nil
		},
	}
}

// ---- helpers -----------------------------------------------------------------

// resolvePath resolves path relative to workspacePath and verifies the result
// stays inside the workspace (sandbox enforcement). Returns an error if the
// resolved path would escape the workspace boundary.
func resolvePath(workspacePath, path string) (string, error) {
	var abs string
	if filepath.IsAbs(path) {
		abs = filepath.Clean(path)
	} else {
		abs = filepath.Clean(filepath.Join(workspacePath, path))
	}
	workspace := filepath.Clean(workspacePath)
	if abs != workspace && !strings.HasPrefix(abs, workspace+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the workspace", path)
	}
	return abs, nil
}

// UpdateTaskStatusTool builds the special update_task_status tool that is
// injected by the NativeEngine. The onUpdate callback is called synchronously
// with the requested status string; it should update the DB and broadcast an
// event to connected clients.
func UpdateTaskStatusTool(onUpdate func(ctx context.Context, status string) error) Tool {
	return Tool{
		Def: ToolDef{
			Type: "function",
			Function: FuncMeta{
				Name:        "update_task_status",
				Description: "Update the status of the current task. Call this to signal progress: use 'in-progress' when starting, 'in-review' when done, 'blocked' when stuck, 'refinement' when you have questions.",
				Parameters: json.RawMessage(`{
					"type":"object",
					"properties":{
						"status":{
							"type":"string",
							"enum":["refinement","in-progress","blocked","in-review"],
							"description":"The new task status"
						}
					},
					"required":["status"]
				}`),
			},
		},
		Execute: func(ctx context.Context, args json.RawMessage) (string, error) {
			var p struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(args, &p); err != nil {
				return "", err
			}
			if err := onUpdate(ctx, p.Status); err != nil {
				return "", fmt.Errorf("update_task_status: %w", err)
			}
			return fmt.Sprintf("Task status updated to %q", p.Status), nil
		},
	}
}
