//go:build !unix

package hindsight

// Orphan reclamation relies on POSIX signals and ps/lsof; on other platforms
// it is a no-op, matching the no-op setProcAttrs there.
func reclaimOrphans(port string) {}

func writePIDFile(pid int) {}

func removePIDFile() {}
