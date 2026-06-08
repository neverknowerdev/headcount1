package utils

import "os"

// IsE2E returns true when running in end-to-end test mode.
func IsE2E() bool {
	return os.Getenv("E2E_MODE") == "true"
}
