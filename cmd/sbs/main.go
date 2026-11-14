package main

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
)

// version is injected at release time via -ldflags "-X main.version=..." (GoReleaser
// sets it from the git tag). When empty, the number in the VERSION file is used.
var version = ""

// fileVersion is the single, hand-edited source of truth for the version number.
//
//go:embed VERSION
var fileVersion string

// versionString returns the human-facing version. A release build returns the
// injected version unchanged. Local builds show the VERSION file number plus the
// git revision they were built from, e.g. "0.1.0 (9e54e8f, dirty)".
func versionString() string {
	if version != "" {
		return version
	}
	base := strings.TrimSpace(fileVersion)
	if base == "" {
		base = "dev"
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return base
	}
	var rev, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if rev == "" {
		return base
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	out := fmt.Sprintf("%s (%s", base, rev)
	if modified == "true" {
		out += ", dirty"
	}
	return out + ")"
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		var ec *exitCodeError
		if errors.As(err, &ec) {
			os.Exit(ec.code)
		}
		// Commands set SilenceErrors; we print to stderr ourselves so that
		// credential_process stdout stays clean.
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
