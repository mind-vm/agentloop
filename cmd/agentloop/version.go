package main

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// version is the release identifier. It is overridden at build time
// (-ldflags "-X main.version=v0.1.0"); an unstamped build falls back to
// whatever the module graph and VCS stamps know.
var version = ""

// versionString renders the version line, preferring an explicit stamp,
// then the module version a `go install` records, then the VCS revision
// a local `go build` embeds.
func versionString() string {
	v := version
	rev := ""

	if info, ok := debug.ReadBuildInfo(); ok {
		if v == "" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			v = info.Main.Version
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if len(s.Value) > 12 {
					rev = s.Value[:12]
				} else {
					rev = s.Value
				}
			case "vcs.modified":
				if s.Value == "true" {
					rev += "-dirty"
				}
			}
		}
	}

	if v == "" {
		v = "devel"
	}
	if rev != "" {
		v += " (" + rev + ")"
	}
	return fmt.Sprintf("agentloop %s %s/%s %s", v, runtime.GOOS, runtime.GOARCH, runtime.Version())
}
