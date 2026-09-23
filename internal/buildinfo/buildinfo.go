package buildinfo

import (
	"runtime/debug"
	"strings"
)

// Version and Revision are overridden by release builds through -ldflags.
var (
	Version  = "dev"
	Revision = "unknown"
)

type Info struct {
	Version  string
	Revision string
}

func Current() Info {
	info := Info{Version: strings.TrimSpace(Version), Revision: strings.TrimSpace(Revision)}
	if info.Version == "" {
		info.Version = "dev"
	}
	if info.Revision == "" {
		info.Revision = "unknown"
	}
	if build, ok := debug.ReadBuildInfo(); ok {
		if info.Version == "dev" && build.Main.Version != "" && build.Main.Version != "(devel)" {
			info.Version = build.Main.Version
		}
		if info.Revision == "unknown" {
			for _, setting := range build.Settings {
				if setting.Key == "vcs.revision" && setting.Value != "" {
					info.Revision = setting.Value
					break
				}
			}
		}
	}
	return info
}
