// Package version exposes build metadata for the netpulse binary.
//
// Release builds inject values at link time:
//
//	go build -ldflags "-X github.com/agarwala-ayush/netpulse/internal/version.Version=v1.0.0 ..."
//
// Builds made with plain `go install` fall back to the module and VCS
// information that the Go toolchain embeds automatically.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Set via -ldflags at build time.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// Info is the resolved build information.
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Date      string `json:"date"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// Get returns build information, filling gaps from debug.ReadBuildInfo.
func Get() Info {
	info := Info{
		Version:   Version,
		Commit:    Commit,
		Date:      Date,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	if info.Version == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		info.Version = bi.Main.Version
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			if info.Commit == "none" && len(s.Value) >= 7 {
				info.Commit = s.Value[:7]
			}
		case "vcs.time":
			if info.Date == "unknown" {
				info.Date = s.Value
			}
		}
	}
	return info
}

// String renders a single human readable line.
func (i Info) String() string {
	return fmt.Sprintf("netpulse %s (commit %s, built %s, %s, %s)",
		i.Version, i.Commit, i.Date, i.GoVersion, i.Platform)
}
