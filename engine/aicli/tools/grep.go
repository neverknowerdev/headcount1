package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"agent-orchestrator/engine/aicli"
)

// Grep searches for regex patterns inside the workspace sandbox.
type Grep struct {
	workspacePath string
	readOnlyDirs  []string
}

// NewGrep creates a Grep tool sandboxed to workspacePath plus optional
// read-only roots.
func NewGrep(workspacePath string, readOnlyDirs ...string) *Grep {
	return &Grep{workspacePath: workspacePath, readOnlyDirs: readOnlyDirs}
}

func (t *Grep) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        string(ToolGrep),
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
	}
}

func (t *Grep) Execute(_ context.Context, args json.RawMessage) (string, error) {
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

	searchPath := t.workspacePath
	if p.Path != "" {
		sp, err := resolveReadPath(t.workspacePath, t.readOnlyDirs, p.Path)
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
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if re.MatchString(line) {
				rel, _ := filepath.Rel(t.workspacePath, filePath)
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
}
