package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"agent-orchestrator/engine/aicli"
)

// ExecCommand runs shell commands inside the workspace sandbox.
type ExecCommand struct {
	workspacePath string
	readOnlyDirs  []string
}

// NewExecCommand creates an ExecCommand tool sandboxed to workspacePath.
func NewExecCommand(workspacePath string, readOnlyDirs ...string) *ExecCommand {
	return &ExecCommand{workspacePath: workspacePath, readOnlyDirs: readOnlyDirs}
}

func (t *ExecCommand) Def() aicli.ToolDef {
	return aicli.ToolDef{
		Type: "function",
		Function: aicli.FuncMeta{
			Name:        "bash",
			Description: "Execute a shell command inside the workspace. Runs with the workspace as the working directory. Only use relative paths — paths outside the workspace are rejected.",
			Parameters: json.RawMessage(`{
				"type":"object",
				"properties":{
					"command":{"type":"string","description":"Shell command to run using relative paths (e.g. \"go test ./...\", \"ls src/\")"}
				},
				"required":["command"]
			}`),
		},
	}
}

func (t *ExecCommand) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if err := validateCommandPaths(t.workspacePath, t.readOnlyDirs, p.Command); err != nil {
		return "", err
	}
	// 60-second hard cap so a misbehaving command can't stall the run forever.
	cmdCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Kernel-level write sandbox (Landlock on Linux, Seatbelt on macOS);
	// see sandbox_exec.go.
	cmd, cleanup, err := sandboxedCommand(cmdCtx, t.workspacePath, p.Command)
	if err != nil {
		return "", err
	}
	if cleanup != nil {
		defer cleanup()
	}
	cmd.Dir = t.workspacePath
	output, err := cmd.CombinedOutput()
	result := string(output)
	if len(result) > 50_000 {
		result = result[:50_000] + "\n... (truncated)"
	}
	if err != nil {
		return fmt.Sprintf("exit error: %v\n%s", err, result), nil
	}
	return result, nil
}
