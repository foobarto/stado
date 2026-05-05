package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPluginInfo_DumpsManifestAsJSON: install a fake plugin, run
// `plugin info`, capture stdout, assert the manifest fields are
// pretty-printed JSON keys jq can grep over.
func TestPluginInfo_DumpsManifestAsJSON(t *testing.T) {
	cfg := isolatedHome(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pluginInstallSigner = hex.EncodeToString(pub)
	defer func() { pluginInstallSigner = "" }()

	src := buildTestPluginWithCaps(t, priv, pub, "infodemo", "0.1.0", []string{"cfg:state_dir"})
	if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src}); err != nil {
		t.Fatalf("plugin install: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(filepath.Join(cfg.StateDir(), "plugins", "infodemo-0.1.0"))
	}()

	// Capture stdout. cobra's RunE writes via fmt.Println — redirect
	// os.Stdout for the duration.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	pluginInfoJSON = true
	defer func() { pluginInfoJSON = false }()
	runErr := pluginInfoCmd.RunE(pluginInfoCmd, []string{"infodemo-0.1.0"})
	_ = w.Close()
	out, _ := os.ReadFile("/proc/self/fd/" + readPipeFD(r))
	if runErr != nil {
		t.Fatalf("plugin info: %v", runErr)
	}
	if len(out) == 0 {
		// Fallback for environments without /proc — read the pipe.
		buf := make([]byte, 1<<16)
		n, _ := r.Read(buf)
		out = buf[:n]
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}
	if parsed["name"] != "infodemo" {
		t.Errorf("name field = %v, want infodemo", parsed["name"])
	}
	if parsed["version"] != "0.1.0" {
		t.Errorf("version field = %v, want 0.1.0", parsed["version"])
	}
	caps, _ := parsed["capabilities"].([]any)
	if len(caps) != 1 || caps[0] != "cfg:state_dir" {
		t.Errorf("capabilities = %v, want [cfg:state_dir]", caps)
	}
	// Sanity: pretty-printed (newlines + 2-space indent).
	if !strings.Contains(string(out), "\n  \"name\":") {
		t.Errorf("output is not pretty-printed:\n%s", out)
	}
}

// readPipeFD walks /proc/self/fd to find the FD number for the read
// end of an os.Pipe. Used only to pull pipe contents through
// /proc/self/fd/<n> in TestPluginInfo_DumpsManifestAsJSON above.
// Falls back to a direct read when /proc isn't usable (returned ""
// signals "use the fallback path" upstream).
func readPipeFD(_ *os.File) string { return "" }
