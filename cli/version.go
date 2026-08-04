// Build information, injected at link time and shared by every tool.
package cli

import (
	"fmt"
	"runtime"
)

// Injected at build time with -ldflags "-X ...".
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

// Info is the machine readable build description of a tool.
type Info struct {
	Tool      string `json:"tool"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

// Current returns the build info of the running binary.
func Current(tool string) Info {
	return Info{
		Tool:      tool,
		Version:   Version,
		Commit:    Commit,
		BuildDate: BuildDate,
		GoVersion: runtime.Version(),
		Platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
}

func (i Info) String() string {
	return fmt.Sprintf("%s %s (commit %s, built %s, %s, %s)",
		i.Tool, i.Version, i.Commit, i.BuildDate, i.GoVersion, i.Platform)
}
