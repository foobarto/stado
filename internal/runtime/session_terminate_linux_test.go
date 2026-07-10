//go:build linux

package runtime

import (
	"os"
	"strings"
	"testing"
)

func TestTerminateOwnedProcessRejectsIdentityMismatch(t *testing.T) {
	err := terminateOwnedProcess(os.Getpid(), "deliberately-wrong-identity")
	if err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("terminateOwnedProcess mismatch error = %v", err)
	}
	if !processAlive(os.Getpid()) {
		t.Fatal("identity mismatch signalled the current test process")
	}
}
