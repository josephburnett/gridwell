package cli

// Version is stamped by the release build from the git tag, its one owner.
var Version = ""

// VersionString never returns empty: an unstamped binary is honestly "dev"
// rather than a drifted number.
func VersionString() string {
	if Version == "" {
		return "dev"
	}
	return Version
}
