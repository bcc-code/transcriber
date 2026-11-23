// Package version reports which source revision the binary was built from.
//
// Docker/CI builds set Commit and BuildTime via -ldflags -X (the build context
// excludes .git, so Go's own VCS stamping has nothing to read there). Local
// `go build` inside a checkout falls back to the vcs.* build settings.
package version

import "runtime/debug"

// Set at link time:
//
//	-X transcriber/internal/version.Commit=<sha> -X transcriber/internal/version.BuildTime=<rfc3339>
var (
	Commit    = ""
	BuildTime = ""
)

// Info is the build identity exposed on GET /version and in the startup log.
type Info struct {
	Commit    string `json:"commit"`
	BuildTime string `json:"build_time"`
	Modified  bool   `json:"modified"`
}

// Get returns the build info, filling gaps from Go's embedded VCS stamp.
func Get() Info {
	info := Info{Commit: Commit, BuildTime: BuildTime}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = s.Value
				}
			case "vcs.time":
				if info.BuildTime == "" {
					info.BuildTime = s.Value
				}
			case "vcs.modified":
				info.Modified = s.Value == "true"
			}
		}
	}
	if info.Commit == "" {
		info.Commit = "unknown"
	}
	return info
}
