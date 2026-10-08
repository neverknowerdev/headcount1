package engine

import (
	"context"
	"errors"
	"fmt"

	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/integration"
	"agent-orchestrator/pkg/runtokens"
	"agent-orchestrator/pkg/secrets"
)

// errVaultLocked means a model call could not be made because the key it
// needs is sealed. The caller waits for an unlock rather than retrying.
var errVaultLocked = errors.New("the model's API key is sealed: its owner is signed out")

// clientFactory builds the client for a model call. Tests replace it to
// point calls at a scripted provider.
type clientFactory func(baseURL, apiKey, model string) *aicli.Client

// callOnce makes one stateless model call that must answer with a single
// validated tool call. The request carries the complete prompt; nothing is
// kept between calls. The result lists every provider round trip, also when
// an error is returned, so the caller can account for all of them.
func callOnce(ctx context.Context, newClient clientFactory, companyID int32, target modelTarget, request aicli.ChatRequest, validate func(aicli.ToolCall) error) (*aicli.OneShotResult, error) {
	apiKey, err := secrets.Default().Decrypt(target.Provider.ApiKeyEncrypted)
	if errors.Is(err, secrets.ErrLocked) {
		return nil, errVaultLocked
	}
	if err != nil {
		return nil, fmt.Errorf("decrypt provider key: %w", err)
	}
	client := newClient(target.Provider.BaseUrl, apiKey, target.Model)
	if target.viaGateway() {
		// A step belongs to a company but to no run, so it authenticates to
		// the in-process gateway with a token of its own, valid for this call.
		token, revoke := runtokens.Default().IssueCompany(companyID)
		defer revoke()
		if token == "" {
			return nil, errors.New("model-group gateway token is unavailable")
		}
		client.ExtraHeaders = map[string]string{runtokens.TokenHeader: token}
	}
	result, err := client.CompleteWithTool(ctx, request, validate)
	var apiErr *aicli.APIErr
	if errors.As(err, &apiErr) && apiErr.Type == integration.VaultLockedErrorType {
		return result, errVaultLocked
	}
	return result, err
}
