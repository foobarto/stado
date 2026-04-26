package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadOrCreateKeyRejectsOversizedExistingKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), KeyFileName)
	body := strings.Repeat("x", int(maxPrivateKeyFileBytes)+1)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadOrCreateKey(path)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversized key error, got %v", err)
	}
}
