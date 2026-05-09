package runtime

// Bounded buffer allocation for host imports.
//
// Several wasm host imports take a guest-supplied output buffer size
// (`outMax`, `bodyMax`, `bufCap`) and call make([]byte, n) directly.
// Without a clamp, a malicious or buggy plugin sending n=2^30 forces
// the host to allocate ~1 GiB; a plugin sending n=math.MaxInt32 panics
// or OOMs depending on the available memory. The 2026-05-09 review
// flagged this in host_http_stream.go (response_read), host_net.go
// (read, recvfrom). host_pty_read and host_proc_read already clamp.
//
// boundedAlloc is the shared helper that consolidates the clamp policy.
// 16 MiB is the per-call ceiling — two orders of magnitude past
// anything legitimate (the largest "real" buffer a plugin would
// reasonably ask for is ~64 KiB; HTTP streaming reads in 32 KiB
// chunks; PTY snapshots are sub-MiB). A higher ceiling on a memory-
// constrained host (CI runner, embedded device) would still be a DoS
// vector; 16 MiB lets a plugin recover from a wide read without
// rounding up to a system OOM.

// maxHostBufferBytes caps a single host-import allocation. Guest-
// supplied sizes above this are silently clamped — the plugin sees
// a short read rather than a host crash, which is the right shape
// for "bug" inputs (a misbehaving plugin) and the right shape for
// "attack" inputs (a malicious plugin tries to OOM the host).
const maxHostBufferBytes = 16 << 20 // 16 MiB

// boundedAlloc returns a buffer of size n, clamped to
// [0, maxHostBufferBytes]. Negative n yields a 0-length buffer
// (caller's read returns 0 immediately, no panic). Used by host
// imports that take a guest-supplied output cap.
func boundedAlloc(n int32) []byte {
	if n <= 0 {
		return nil
	}
	if int64(n) > int64(maxHostBufferBytes) {
		return make([]byte, maxHostBufferBytes)
	}
	return make([]byte, n)
}
