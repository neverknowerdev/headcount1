package engine

import (
	"context"
	"fmt"
	"sort"

	"agent-orchestrator/db"
	"agent-orchestrator/pkg/logging"
	"agent-orchestrator/pkg/secrets"
)

// loadEnvironmentSecrets returns the default company runtime environment.
// Secret values are registered for redaction by Decrypt before they enter the
// shell; plain variables use DecryptRaw so ordinary values are not redacted.
func (e *NativeEngine) loadEnvironmentSecrets(ctx context.Context, companyID int32, logger *logging.ProxyLogger) (map[string]string, []string) {
	values := map[string]string{}
	env, rows, err := e.q.DefaultEnvironmentSecrets(ctx, companyID)
	if err != nil {
		e.logInfo(logger, "Warning: could not resolve task environment: "+err.Error())
		return values, nil
	}
	for _, row := range rows {
		var value string
		if row.Kind == db.EnvEntryVariable {
			value, err = secrets.Default().DecryptRaw(row.ValueEncrypted)
		} else {
			value, err = secrets.Default().Decrypt(row.ValueEncrypted)
		}
		if err != nil {
			e.logInfo(logger, fmt.Sprintf("Warning: environment value %s unavailable: %v", row.Name, err))
			continue
		}
		values[row.Name] = value
	}
	if len(values) == 0 {
		return values, nil
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	e.logInfo(logger, fmt.Sprintf("Loaded %d value(s) from the %q environment", len(names), env.Name))
	return values, names
}
