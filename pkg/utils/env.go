package utils

import (
	"os"
	"strings"
)

// IsE2E returns true when running in end-to-end test mode.
func IsE2E() bool {
	return os.Getenv("E2E_MODE") == "true"
}

// Deploy environments. A server's environment decides which deploy events it
// acts on: production applies main/release events (filtered by the deploy_source
// setting), staging applies any branch/PR event.
const (
	EnvProduction = "production"
	EnvStaging    = "staging"
)

// CurrentEnv returns this server's deploy environment from HEADCOUNT1_ENV,
// defaulting to production. Only "staging" flips it; anything else (including
// unset) is production, so a misconfigured value never accidentally opens a
// server up to arbitrary-branch deploys.
func CurrentEnv() string {
	if strings.ToLower(strings.TrimSpace(os.Getenv("HEADCOUNT1_ENV"))) == EnvStaging {
		return EnvStaging
	}
	return EnvProduction
}

// IsStaging reports whether this server is a staging deployment.
func IsStaging() bool {
	return CurrentEnv() == EnvStaging
}

// DeployAPIKey is the shared secret a deploy webhook must present (header
// X-Deploy-Key). Empty means no key is configured, which disables the deploy
// webhook entirely — a server with no key set can never be deployed to over
// HTTP, which is the safe default.
func DeployAPIKey() string {
	return os.Getenv("HEADCOUNT1_DEPLOY_API_KEY")
}

// DeployDownloadToken is an optional bearer token the server uses when
// downloading a release-asset binary (needed if the releases repo is private).
// Empty means anonymous download.
func DeployDownloadToken() string {
	return os.Getenv("HEADCOUNT1_DEPLOY_DOWNLOAD_TOKEN")
}
