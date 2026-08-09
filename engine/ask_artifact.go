package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/runtokens"
	"agent-orchestrator/pkg/secrets"
)

// askArtifact wires engine storage, model selection, accounting, and logging
// into the per-tool ArtifactReader implementation.
func (e *NativeEngine) askArtifact(
	ctx context.Context,
	runID, rootTaskID int32,
	provider db.LLMProvider,
	sessionModel string,
	filename, question string,
	logger *logging.ProxyLogger,
) (string, error) {
	reader := &tools.ArtifactReader{
		FindArtifact: func(findCtx context.Context, name string) (string, error) {
			arts, err := e.q.ListArtifactsByTaskTree(findCtx, rootTaskID)
			if err != nil {
				return "", err
			}
			for i := len(arts) - 1; i >= 0; i-- {
				if arts[i].Filename == name {
					return arts[i].Content, nil
				}
			}
			return "", fmt.Errorf("artifact %q not found — call list_artifacts to see what exists", name)
		},
		ResolveTarget: func(targetCtx context.Context) (tools.ArtifactReaderTarget, error) {
			readerProvider, model := e.resolvePurposeModel(targetCtx, e.ownerUserIDForCompanyOfTask(targetCtx, rootTaskID), db.PurposeAskArtifact, provider, sessionModel)
			apiKey := ""
			if readerProvider.ApiKeyEncrypted != "" {
				var err error
				apiKey, err = secrets.Default().Decrypt(readerProvider.ApiKeyEncrypted)
				if err != nil {
					return tools.ArtifactReaderTarget{}, fmt.Errorf("artifact reader: decrypt provider key: %w", err)
				}
			}
			target := tools.ArtifactReaderTarget{
				BaseURL: readerProvider.BaseUrl,
				APIKey:  apiKey,
				Model:   model,
			}
			if isModelGroupProxyBaseURL(readerProvider.BaseUrl) && runID > 0 {
				// A fallback to the asking session's model group uses a synthetic
				// localhost provider with no API key, so authenticate it with the
				// same short-lived run token as the main session.
				token := runtokens.Default().Issue(runID)
				target.Cleanup = func() { runtokens.Default().Revoke(runID) }
				target.ExtraHeaders = map[string]string{
					runtokens.TokenHeader: token,
					"X-Run-ID":            fmt.Sprintf("%d", runID),
					"X-Proxy-Log-Mode":    "switches-only",
				}
			}
			return target, nil
		},
		RecordUsage: func(statCtx context.Context, usage aicli.Usage) {
			if runID == 0 {
				return
			}
			if err := e.q.AddRunTokenStats(statCtx, runID, db.RunTokenStats{
				PromptTokens:     usage.PromptTokens,
				CompletionTokens: usage.CompletionTokens,
			}); err != nil {
				fmt.Printf("Warning: failed to record ask_artifact token stats: %v\n", err)
			}
		},
		LogExchange: func(name, model, q, prompt, answer string, promptTokens, completionTokens int) {
			e.logAskArtifact(logger, runID, name, model, q, prompt, answer, promptTokens, completionTokens)
		},
	}
	return reader.Answer(ctx, filename, question)
}

func isModelGroupProxyBaseURL(baseURL string) bool {
	u, err := url.Parse(baseURL)
	return err == nil && strings.HasPrefix(u.Path, "/api/proxy/group/")
}

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

func (e *NativeEngine) logAskArtifact(logger *logging.ProxyLogger, runID int32, filename, model, question, prompt, answer string, promptTokens, completionTokens int) {
	if logger == nil {
		return
	}
	logName := fmt.Sprintf("ask-artifact-%d-%d.log", runID, time.Now().UnixMilli())
	logPath := filepath.Join(filepath.Dir(logger.FilePath()), logName)
	content := fmt.Sprintf(`=== ask_artifact ===
Time: %s
Asking run: #%d
Artifact: %s
Reader model: %s
Question: %s
Usage: prompt_tokens=%d completion_tokens=%d

--- Reader prompt (artifact content as sent) ---
%s

--- Answer ---
%s
`, time.Now().UTC().Format(time.RFC3339), runID, filename, model, question, promptTokens, completionTokens, prompt, answer)
	if err := os.WriteFile(logPath, []byte(content), 0644); err != nil {
		e.logInfo(logger, fmt.Sprintf("Warning: failed to write ask_artifact log: %v", err))
		return
	}
	e.logInfo(logger, fmt.Sprintf("ask_artifact %q (model %s) — full exchange in %s", filename, model, logName))
}
