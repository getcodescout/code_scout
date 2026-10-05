// Package app holds the instance's identity: what version it is, and what was
// built to produce it.
//
// It deliberately imports nothing. Every layer reads from here — main for the
// startup log, the health handler, and the views — and a package that imported
// anything would eventually be unimportable from one of them.
package app

// Version is the released version of this dashboard, and it is the single
// source of truth for it.
//
// It is a constant in the source rather than something the linker injects from
// a git tag, so that a local build, a `go install` and a release image all
// report the same honest answer instead of "dev". The tag is checked against
// this on release: CI refuses to publish when `v1.2.0` is cut from a tree that
// still says 1.1.0 here.
//
// Bump it in the same commit that dates the matching CHANGELOG.md section.
const Version = "1.2.0"

// Commit and BuildTime describe the build rather than the release, and are
// injected at link time with -ldflags. They default to "-" so a `go build` with
// no flags is still a working binary that simply admits it does not know.
//
// Three build paths set these — the Makefile's `build` and `deploy` targets and
// the Dockerfile — and all three have to agree. They did not before: the
// Dockerfile passed a build arg named VERSION into a variable named BranchName,
// which is the kind of drift that survives precisely because nothing displayed
// the result.
var (
	Commit    = "-"
	BuildTime = "-"
)
