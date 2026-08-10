package engine

import (
	"context"
	"fmt"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/secrets"
)

// askArtifact wires engine storage, model selection, accounting, and logging
// into the per-tool ArtifactReader implementation.
func (e *NativeEngine) askArtifact(
	ctx context.Context,
	runID, rootTaskID int32,
	provider db.LLMProvider,
	sessionModel string,
	gatewayAuth runGatewayAuth,
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
				BaseURL:      readerProvider.BaseUrl,
				APIKey:       apiKey,
				Model:        model,
				ProviderName: readerProvider.Name,
			}
			if err := gatewayAuth.configureClientTarget(&target, readerProvider); err != nil {
				return tools.ArtifactReaderTarget{}, fmt.Errorf("artifact reader: %w", err)
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
		LogRequest: func(model, provider string, body []byte) {
			if logger != nil {
				logger.LogRequest(model, string(aicli.ToolAskArtifact), provider, body)
			}
		},
		LogResponse: func(model, provider string, body []byte, usage aicli.Usage) {
			if logger != nil {
				logger.LogResponse(model, provider, 200, body, "", logging.Usage{
					PromptTokens:     usage.PromptTokens,
					CompletionTokens: usage.CompletionTokens,
					TotalTokens:      usage.TotalTokens,
					ReasoningTokens:  usage.CompletionTokensDetails.ReasoningTokens,
					CachedTokens:     usage.PromptTokensDetails.CachedTokens,
				})
			}
		},
	}
	return reader.Answer(ctx, filename, question)
}
