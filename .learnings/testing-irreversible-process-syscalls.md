# Testing irreversible process-wide syscalls (landlock, seccomp, setuid…)

## Problem

Some kernel APIs narrow the calling process's privileges and can never be
undone (landlock_restrict_self, seccomp_set_mode_filter, capget+drop, …).
Applying them in a `go test` binary would break every subsequent test in the
same run — the process can't re-open files it needs to read the test source,
write coverage profiles, etc.

## Pattern: self-re-exec with an env marker

In the test, check an env var. If set, run the restricted body and
`os.Exit(N)` to signal outcome. If not, `exec.Command(os.Executable(), …)`
with the env set, inspect the child's exit code.

```go
func TestApplyFoo(t *testing.T) {
    if os.Getenv("STADO_FOO_SUBPROC") == "1" {
        childBody()
        return // unreached
    }
    exe, _ := os.Executable()
    cmd := exec.Command(exe, "-test.run=TestApplyFoo")
    cmd.Env = append(os.Environ(), "STADO_FOO_SUBPROC=1")
    out, err := cmd.CombinedOutput()
    status := err.(*exec.ExitError).Error()
    switch {
    case strings.Contains(status, "exit status 42"):
        // success
    case strings.Contains(status, "exit status 43"):
        t.Skip("feature unsupported")
    default:
        t.Errorf("subprocess: status=%q out=%s", status, out)
    }
}

func childBody() {
    if err := applyRestriction(); errors.Is(err, ErrUnsupported) {
        os.Exit(43)
    } else if err != nil {
        os.Exit(1)
    }
    if !restrictionIsEnforced() {
        os.Exit(2)
    }
    os.Exit(42)
}
```

## Tips

- Pick **distinct exit codes** per outcome (42=pass, 43=skip, 1-41=specific
  failure class). Helps debug without stdin/stdout capture.
- `-test.run=<exactName>` narrows the child to only the one test —
  otherwise the child re-runs the entire suite (including itself),
  recursing.
- `CombinedOutput()` captures both streams for the failure message.
- `t.Skip` on "feature not supported in this kernel" — CI might run on old
  kernels; the test itself isn't what's broken.
- Don't try to unwrap the exit code with `ee.ExitCode()` — it works, but
  matching the stringified `"exit status N"` is shorter and survives
  stderr noise in the error message.

## Example use

`internal/sandbox/landlock_linux_test.go` applies a FSRead-only-for-/tmp
landlock policy in the child and proves reading /etc/hosts is blocked.
Without this pattern the test would have to choose between testing
landlock or running any subsequent tests — not both.
