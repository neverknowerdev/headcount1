package migrations

import "embed"

// Files contains the dialect-specific SQL migration directories shipped with
// the server binary. The runtime selects the directory that matches the open
// database connection, while Atlas CLI consumes the same files directly.
//
//go:embed postgres sqlite
var Files embed.FS
