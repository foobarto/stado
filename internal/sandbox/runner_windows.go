//go:build windows

package sandbox

// detectList on Windows currently has no native sandbox runner. Phase
// 3.6 will add a job-object + restricted-token based runner; until
// then we fall back to NoneRunner so the binary still runs commands
// (without OS-level isolation).
func detectList() []Runner {
	return []Runner{NoneRunner{}}
}
