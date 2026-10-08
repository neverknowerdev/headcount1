package engine

import (
	"fmt"

	"agent-orchestrator/pkg/runtokens"
)

// modelGroupGatewayHeaders authenticates an executor session to the
// in-process model-group gateway and names its run, so the gateway can add
// the model switches it makes to the session's log.
func modelGroupGatewayHeaders(runID int32, token string) map[string]string {
	return map[string]string{
		runtokens.TokenHeader: token,
		"X-Run-ID":            fmt.Sprintf("%d", runID),
	}
}
