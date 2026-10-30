package stacks

// writeTrust names the users and groups whose write access to an imported
// compose file is expected: root, the service user and the docker group, who
// can already run arbitrary containers.
type writeTrust struct {
	uids, gids map[uint32]bool
}

// importWarnings lists the paths, from the compose file up to /, that someone
// outside the trusted users and groups can modify. Whoever can replace the
// file, or a folder on its way, decides what runs as root on the next up.
// Empty when nothing is wrong or the platform cannot tell.
func importWarnings(composePath string) []string {
	return untrustedWriters(composePath, defaultWriteTrust())
}
