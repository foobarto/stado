//go:build !linux

package sandbox

// detectList for non-Linux platforms. macOS and Windows specific runners
// land in PLAN.md §3.5 / §3.6 follow-ups.
func detectList() []Runner {
	return []Runner{NoneRunner{}}
}
