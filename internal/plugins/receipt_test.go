package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallReceiptParserRejectsUnknownFieldsAndTrailingValues(t *testing.T) {
	state := t.TempDir()
	path := receiptPath(state)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	key := "local-" + strings.Repeat("a", 64)
	for _, raw := range []string{
		`[{"schema":1,"plugins_root":"/tmp/plugins","store_key":"` + key + `","authority":"forged"}]`,
		`[{"schema":1,"plugins_root":"/tmp/plugins","store_key":"` + key + `"}] []`,
	} {
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadInstallReceipts(path); err == nil {
			t.Fatalf("receipt parser accepted %q", raw)
		}
	}
}
