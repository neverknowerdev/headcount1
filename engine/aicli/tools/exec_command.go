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
	// extraEnv is appended to the child's (scrubbed) environment — the
	// task's environment secrets. The agent can use them ($API_KEY) but any
	// echo of the values is redacted from the tool output upstream.
	extraEnv map[string]string
}

// NewExecCommand creates an ExecCommand tool sandboxed to workspacePath.
func NewExecCommand(workspacePath string, readOnlyDirs ...string) *ExecCommand {
	return &ExecCommand{workspacePath: workspacePath, readOnlyDirs: readOnlyDirs}
}

// SetExtraEnv sets additional NAME=value pairs injected into every command's
// environment (after the server-secret scrub, which only filters the server's
// own inherited env — deliberate injections are exempt).
func (t *ExecCommand) SetExtraEnv(env map[string]string) { t.extraEnv = env }

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
	cmd, cleanup, err := sandboxedCommand(cmdCtx, t.workspacePath, p.Command, t.readOnlyDirs)
	if err != nil {
		return "", err
	}
	if cleanup != nil {
		defer cleanup()
	}
	cmd.Dir = t.workspacePath
	// Never hand the agent the server's secrets via the environment. The
	// Landlock re-exec child inherits this scrubbed set, so `env` /
	// /proc/self/environ can't leak the boot key, Vault token, etc. The
	// dedicated-uid path may have already set cmd.Env (scrubbed + a HOME
	// pointed at the sandbox uid's home); don't clobber it.
	if cmd.Env == nil {
		cmd.Env = scrubbedEnv()
	}
	// Task environment secrets ride after the scrubbed base env, so they win
	// over any same-named inherited var. This is the "use but not see" path:
	// the value exists only in the child process's environment; the agent's
	// view of it (tool output, message history, run logs) is redacted.
	for name, value := range t.extraEnv {
		cmd.Env = append(cmd.Env, name+"="+value)
	}
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
