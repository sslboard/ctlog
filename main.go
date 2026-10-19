// Command ctlog lists hostnames seen in Certificate Transparency logs, using
// the SSLBoard hostname API.
package main

import (
	"os"
	"runtime/debug"

	"github.com/sslboard/ctlog/internal/cli"
)

// version is set by GoReleaser via -ldflags "-X main.version=...".
var version = ""

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr, os.Getenv, resolveVersion()))
}

// resolveVersion falls back to the module version for "go install ...@vX.Y.Z"
// builds, which don't go through GoReleaser.
func resolveVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
