package engine

import (
	"fmt"
	"net/url"
	"strings"

	"agent-orchestrator/db"
	"agent-orchestrator/engine/aicli"
	"agent-orchestrator/engine/aicli/tools"
	"agent-orchestrator/pkg/runtokens"
)

// runGatewayAuth carries the one gateway credential for an entire native run.
// Nested LLM calls must reuse it; issuing another token for the same run
// invalidates the active token in the registry.
type runGatewayAuth struct {
	runID int32
	token string
}

func (a runGatewayAuth) configure(client *aicli.Client, provider db.LLMProvider) error {
	if !isModelGroupProxyBaseURL(provider.BaseUrl) {
		return nil
	}
	if a.token == "" {
		return fmt.Errorf("model-group gateway token is unavailable")
	}
	client.ExtraHeaders = modelGroupGatewayHeaders(a.runID, a.token)
	return nil
}

func (a runGatewayAuth) configureClientTarget(target *tools.ArtifactReaderTarget, provider db.LLMProvider) error {
	if !isModelGroupProxyBaseURL(provider.BaseUrl) {
		return nil
	}
	if a.token == "" {
		return fmt.Errorf("model-group gateway token is unavailable")
	}
	target.ExtraHeaders = modelGroupGatewayHeaders(a.runID, a.token)
	return nil
}

func isModelGroupProxyBaseURL(baseURL string) bool {
	u, err := url.Parse(baseURL)
	return err == nil && strings.HasPrefix(u.Path, "/api/proxy/group/")
}

func modelGroupGatewayHeaders(runID int32, token string) map[string]string {
	return map[string]string{
		runtokens.TokenHeader: token,
		"X-Run-ID":            fmt.Sprintf("%d", runID),
		"X-Proxy-Log-Mode":    "switches-only",
	}
}
