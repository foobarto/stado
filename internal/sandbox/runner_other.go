//go:build !linux && !darwin && !windows

package sandbox

// detectList for unsupported-platform fallback (neither Linux, darwin,
// nor windows — e.g. openbsd, freebsd). Always returns NoneRunner until
// we add platform-specific runners. Linux / darwin / windows use
// dedicated runner_<os>.go files.
func detectList() []Runner {
	return []Runner{NoneRunner{}}
}
