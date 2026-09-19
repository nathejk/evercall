// Package vcs exposes build-time provenance.
//
// The values are injected by the linker (`-ldflags "-X ..."`) in the
// Dockerfile's build stage, from the four build-args the GitHub Actions
// workflow passes. In a plain `go build`/`go run` they keep their defaults, so
// a dev binary is always distinguishable from a published one.
package vcs

var (
	Version     = "dev"
	Commit      = "unknown"
	Branch      = "unknown"
	BuildNumber = "0"
)

// String renders the provenance as one log-friendly line.
func String() string {
	return "version=" + Version +
		" commit=" + Commit +
		" branch=" + Branch +
		" build=" + BuildNumber
}
