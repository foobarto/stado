package main

// `stado plugin init <name>` — scaffold a new plugin project.
// Writes a minimal Go plugin directory matching the
// plugins/examples/hello-go layout so the user can go from zero to
// compile in one command. Template is parameterised by the plugin
// name (becomes the module path + tool name + manifest name).
//
// This file is the ONLY place new-plugin content is written, so a
// user can compare their scaffold against this template and know
// what the system expects at the ABI level.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/spf13/cobra"
)

var (
	pluginInitDir   string
	pluginInitForce bool
)

var pluginInitCmd = &cobra.Command{
	Use:   "init <name>",
	Short: "Scaffold a new plugin project — Go wasip1 template",
	Long: "Creates a new directory (--dir, default: ./<name>) with the files\n" +
		"needed to build a signed stado plugin:\n\n" +
		"  <name>/\n" +
		"    go.mod\n" +
		"    main.go                     — wasm ABI boilerplate + single demo tool\n" +
		"    plugin.manifest.template.json\n" +
		"    build.sh                    — compile + sign script\n" +
		"    README.md                   — next steps\n\n" +
		"After scaffolding: `cd <name>`, generate a signing key via\n" +
		"`stado plugin gen-key <name>.seed`, then ./build.sh to produce\n" +
		"the signed plugin.wasm + plugin.manifest.json pair.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if !validPluginName(name) {
			return fmt.Errorf("init: plugin name must be [a-z0-9-]+, got %q", name)
		}
		dir := pluginInitDir
		if dir == "" {
			dir = name
		}
		root, err := openPluginInitRoot(dir, pluginInitForce)
		if err != nil {
			return err
		}
		defer func() { _ = root.Close() }()

		files := []struct {
			name      string
			body      string
			mode      os.FileMode
			exactMode bool
		}{
			{name: "go.mod", body: renderGoMod(name), mode: 0o644},
			{name: "main.go", body: renderMainGo(name), mode: 0o644},
			{name: "plugin.manifest.template.json", body: renderManifest(name), mode: 0o644},
			{name: "build.sh", body: renderBuildSh(name), mode: 0o755, exactMode: true},
			{name: "README.md", body: renderReadme(name), mode: 0o644},
		}
		for _, f := range files {
			write := workdirpath.WriteRootFileAtomic
			if f.exactMode {
				write = workdirpath.WriteRootFileAtomicExactMode
			}
			path := filepath.Join(dir, f.name)
			if err := write(root, f.name, []byte(f.body), f.mode); err != nil {
				return fmt.Errorf("init: write %s: %w", path, err)
			}
		}

		fmt.Fprintf(os.Stderr, "scaffolded plugin %q at %s\n", name, dir)
		fmt.Fprintln(os.Stderr, "next steps:")
		fmt.Fprintf(os.Stderr, "  cd %s\n", dir)
		fmt.Fprintf(os.Stderr, "  stado plugin gen-key %s.seed\n", name)
		fmt.Fprintln(os.Stderr, "  ./build.sh")
		return nil
	},
}

func openPluginInitRoot(dir string, force bool) (*os.Root, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("init: output dir is empty")
	}
	cleanDir := filepath.Clean(dir)
	if cleanDir == "." {
		info, err := os.Lstat(cleanDir)
		if err != nil {
			return nil, fmt.Errorf("init: stat %s: %w", dir, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("init: output dir is a symlink: %s", dir)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("init: output path is not a directory: %s", dir)
		}
		if !force {
			return nil, fmt.Errorf("init: %s already exists (use --force to overwrite)", dir)
		}
		root, err := os.OpenRoot(cleanDir)
		if err != nil {
			return nil, fmt.Errorf("init: open %s: %w", dir, err)
		}
		return root, nil
	}

	parent := filepath.Dir(cleanDir)
	name := filepath.Base(cleanDir)
	if !filepath.IsLocal(name) || strings.ContainsAny(name, `/\`) || name == "." || name == ".." || strings.Contains(name, "\x00") {
		return nil, fmt.Errorf("init: invalid output dir %q", dir)
	}
	if err := mkdirAllNoSymlink(parent, 0o750); err != nil {
		return nil, fmt.Errorf("init: mkdir %s: %w", parent, err)
	}
	parentRoot, err := os.OpenRoot(parent)
	if err != nil {
		return nil, fmt.Errorf("init: open %s: %w", parent, err)
	}
	defer func() { _ = parentRoot.Close() }()

	info, err := parentRoot.Lstat(name)
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return nil, fmt.Errorf("init: output dir is a symlink: %s", dir)
	case err == nil && !info.IsDir():
		return nil, fmt.Errorf("init: output path is not a directory: %s", dir)
	case err == nil && !force:
		return nil, fmt.Errorf("init: %s already exists (use --force to overwrite)", dir)
	case err == nil:
	case os.IsNotExist(err):
		if err := parentRoot.Mkdir(name, 0o750); err != nil {
			return nil, fmt.Errorf("init: mkdir %s: %w", dir, err)
		}
	default:
		return nil, fmt.Errorf("init: stat %s: %w", dir, err)
	}

	root, err := parentRoot.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("init: open %s: %w", dir, err)
	}
	return root, nil
}

// validPluginName enforces the charset used in directory names,
// manifest IDs, and Go module paths. Keeps the template simple —
// weird characters in a name would need escaping in several places
// and that's more trouble than it's worth.
func validPluginName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-':
		default:
			return false
		}
	}
	return true
}

func renderGoMod(name string) string {
	return `module github.com/you/` + name + `

go 1.25
`
}

func renderManifest(name string) string {
	// Minimal manifest — same shape as hello-go's template. Users
	// who need session or memory capabilities add them to the
	// capabilities array (see SECURITY.md + DESIGN.md for the set).
	return `{
  "name": "` + name + `",
  "version": "0.1.0",
  "author": "YOUR-NAME",
  "author_pubkey_fpr": "",
  "wasm_sha256": "",
  "capabilities": [],
  "tools": [
    {
      "name": "greet",
      "description": "Demo tool scaffolded by stado plugin init — edit me!",
      "schema": "{\"type\":\"object\",\"properties\":{\"name\":{\"type\":\"string\"}}}"
    }
  ],
  "min_stado_version": "0.1.0",
  "timestamp_utc": "` + pluginInitTimestamp() + `",
  "nonce": "` + name + `-init"
}
`
}

func renderBuildSh(name string) string {
	return `#!/usr/bin/env bash
# build.sh — compile main.go to plugin.wasm and sign the manifest.
# Generated by 'stado plugin init ` + name + `'.

set -euo pipefail

STADO="${STADO:-stado}"
SEED="${SEED:-` + name + `.seed}"

if [[ ! -f "$SEED" ]]; then
  echo "$SEED not found. Generate with:" >&2
  echo "  $STADO plugin gen-key $SEED" >&2
  exit 1
fi

echo "→ seeding plugin.manifest.json from template"
cp plugin.manifest.template.json plugin.manifest.json

echo "→ compiling main.go (GOOS=wasip1 -buildmode=c-shared)"
rm -f plugin.wasm
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
echo "  → plugin.wasm ($(stat -c '%s bytes' plugin.wasm 2>/dev/null || stat -f '%z bytes' plugin.wasm))"

echo "→ signing plugin.manifest.json"
"$STADO" plugin sign plugin.manifest.json --key "$SEED"
`
}

func renderReadme(name string) string {
	return `# ` + name + `

Stado plugin scaffold. Generated by ` + "`stado plugin init " + name + "`" + `.

## Build

` + "```sh\n" +
		`# One-time: generate a signing key. Keep the .seed file offline.
stado plugin gen-key ` + name + `.seed

# Compile + sign.
./build.sh

# First-time trust + install.
stado plugin trust <pubkey-hex-from-gen-key>
stado plugin install .

# Run the demo tool.
stado plugin run ` + name + `-0.1.0 greet '{"name":"world"}'
` + "```\n" +
		`
## Next steps

- Edit ` + "`main.go`" + ` to replace the ` + "`greet`" + ` demo with the tool you
  actually want. The wasm-export boilerplate (` + "`stado_alloc`" + `,
  ` + "`stado_free`" + `, ` + "`stado_tool_<name>`" + `) stays stable across tools.
- Edit ` + "`plugin.manifest.template.json`" + ` to declare your tool's schema
  and any capabilities you need (` + "`fs:read:<path>`" + `, ` + "`session:read`" + `,
  ` + "`memory:propose`" + `, ` + "`llm:invoke:<budget>`" + `, etc. — see DESIGN.md for
  the full set).
- Bump the manifest's ` + "`version`" + ` when you change anything; stado's
  rollback guard rejects installs that go backwards.

## Publishing

See [SECURITY.md → Plugin-publish cookbook](../SECURITY.md) for the
offline signing + distribution flow.
`
}

// renderMainGo emits a minimal Go source file with the expected
// wasm ABI surface. The demo tool echoes "Hello, <name>!" exactly
// like plugins/examples/hello-go/ so users can compare output.
func renderMainGo(name string) string {
	return `// ` + name + ` — stado plugin scaffolded by 'stado plugin init'.
//
// Build target: GOOS=wasip1 + -buildmode=c-shared. The wasm ABI
// exports (stado_alloc, stado_free, stado_tool_*) and imports
// (//go:wasmimport stado ...) are what the stado runtime talks to.
package main

import (
	"encoding/json"
	"sync"
	"unsafe"
)

// main is required for buildmode=c-shared but never runs — the host
// instantiates the module and calls our exports directly.
func main() {}

//go:wasmimport stado stado_log
func stadoLog(levelPtr, levelLen, msgPtr, msgLen uint32)

func logInfo(msg string) {
	level := []byte("info")
	m := []byte(msg)
	stadoLog(
		uint32(uintptr(unsafe.Pointer(&level[0]))), uint32(len(level)),
		uint32(uintptr(unsafe.Pointer(&m[0]))), uint32(len(m)),
	)
}

var pinned sync.Map

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 {
	if size <= 0 {
		return 0
	}
	buf := make([]byte, size)
	ptr := uintptr(unsafe.Pointer(&buf[0]))
	pinned.Store(ptr, buf)
	return int32(ptr)
}

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) {
	pinned.Delete(uintptr(ptr))
	_ = size
}

// greet is the demo tool. Replace with whatever your plugin does.
type greetArgs struct {
	Name string ` + "`json:\"name\"`" + `
}
type greetResult struct {
	Message string ` + "`json:\"message\"`" + `
}

//go:wasmexport stado_tool_greet
func stadoToolGreet(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	logInfo("greet invoked")

	args := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(argsPtr))), int(argsLen))
	var a greetArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return -1
		}
	}
	who := a.Name
	if who == "" {
		who = "world"
	}
	payload, err := json.Marshal(greetResult{Message: "Hello, " + who + "!"})
	if err != nil {
		return -1
	}
	if int32(len(payload)) > resultCap {
		return -1
	}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(resultPtr))), int(resultCap))
	copy(dst, payload)
	return int32(len(payload))
}
`
}

// pluginInitTimestamp returns the current time in the RFC3339 format
// the manifest expects. Kept as a function so tests can override via
// build-tagged shims if they ever need a deterministic value.
func pluginInitTimestamp() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

func init() {
	pluginInitCmd.Flags().StringVar(&pluginInitDir, "dir", "",
		"Destination directory (default: ./<name>)")
	pluginInitCmd.Flags().BoolVar(&pluginInitForce, "force", false,
		"Overwrite the destination if it already exists")
	pluginCmd.AddCommand(pluginInitCmd)
}

// Silence linter: unused import when strings isn't referenced.
var _ = strings.Contains
