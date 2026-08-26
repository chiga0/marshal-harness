// Package buildinfo exposes immutable build metadata injected with linker
// flags. Development builds use explicit sentinel values.
package buildinfo

import "runtime"

// Milestone is the highest implementation milestone reached by this source
// tree. It is the single source of truth reported by `marshal doctor`.
const Milestone = "6"

var (
	version     = "dev"
	commit      = "unknown"
	buildDate   = "unknown"
	selfProfile = "unprofiled"
)

// Info describes the running marshal binary.
type Info struct {
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	BuildDate   string `json:"buildDate"`
	GoVersion   string `json:"goVersion"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	SelfProfile string `json:"selfProfile"`
}

// Current returns this binary's build information.
func Current() Info {
	return Info{
		Version:     version,
		Commit:      commit,
		BuildDate:   buildDate,
		GoVersion:   runtime.Version(),
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		SelfProfile: selfProfile,
	}
}
