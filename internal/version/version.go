// Package version holds build information, set with -ldflags:
//
//	go build -ldflags "-X nexostream/internal/version.Version=0.1.0 -X nexostream/internal/version.Commit=abc123"
package version

var (
	Version = "0.1.0-dev"
	Commit  = "unknown"
)

// String returns "version (commit)".
func String() string { return Version + " (" + Commit + ")" }
