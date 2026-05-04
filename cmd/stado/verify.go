package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/audit"
)

var (
	verifyJSON    bool
	verifyShowKey bool
)

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Print build provenance + embedded trust info",
	Long: "Display the build info embedded at compile time:\n" +
		"- module version + VCS commit + VCS revision time\n" +
		"- Go toolchain version + target os/arch\n" +
		"- the binary's own mtime (when run from a release tarball, usually the\n" +
		"  release's build timestamp).\n\n" +
		"With --show-builtin-keys, also prints the minisign public key + key id\n" +
		"this binary was compiled with — the roots `stado self-update` verifies\n" +
		"checksums.txt.minisig against. DESIGN §10.4.\n\n" +
		"Combined with `go build -trimpath -buildvcs=true`, this lets an auditor\n" +
		"confirm a binary came from the expected source commit.",
	RunE: func(cmd *cobra.Command, args []string) error {
		info := collectBuildInfo()
		if verifyJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(info)
		}
		fmt.Printf("stado version:    %s\n", info.Version)
		fmt.Printf("vcs commit:       %s\n", info.Commit)
		fmt.Printf("vcs time:         %s\n", info.CommitTime)
		fmt.Printf("vcs modified:     %v\n", info.Modified)
		fmt.Printf("go version:       %s\n", info.GoVersion)
		fmt.Printf("target:           %s/%s\n", info.GOOS, info.GOARCH)
		fmt.Printf("main module path: %s\n", info.MainModule)
		if verifyShowKey {
			fmt.Println()
			fmt.Println("embedded trust roots:")
			if info.MinisignPubkey == "" {
				fmt.Println("  minisign pubkey:  (not pinned — release builds seed via ldflags)")
			} else {
				fmt.Printf("  minisign pubkey:  %s\n", info.MinisignPubkey)
			}
			fmt.Printf("  minisign keyid:   %d\n", info.MinisignKeyID)
		}
		return nil
	},
}

type buildInfo struct {
	Version        string `json:"version"`
	Commit         string `json:"commit"`
	CommitTime     string `json:"commitTime"`
	Modified       bool   `json:"modified"`
	GoVersion      string `json:"goVersion"`
	GOOS           string `json:"goos"`
	GOARCH         string `json:"goarch"`
	MainModule     string `json:"mainModule"`
	MinisignPubkey string `json:"minisignPubkey,omitempty"`
	MinisignKeyID  uint64 `json:"minisignKeyId,omitempty"`
}

func collectBuildInfo() buildInfo {
	out := buildInfo{
		Version:        version,
		GoVersion:      runtime.Version(),
		GOOS:           runtime.GOOS,
		GOARCH:         runtime.GOARCH,
		MinisignPubkey: audit.EmbeddedMinisignPubkey,
		MinisignKeyID:  audit.EmbeddedMinisignKeyID,
	}
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return out
	}
	out.MainModule = bi.Main.Path
	if out.Version == "" || out.Version == "0.0.0-dev" {
		out.Version = bi.Main.Version
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			out.Commit = s.Value
		case "vcs.time":
			out.CommitTime = s.Value
		case "vcs.modified":
			out.Modified = s.Value == "true"
		}
	}
	// Final fallback: when neither ldflags nor a versioned go-install
	// gave us something readable (`(devel)` or empty), synthesise a
	// version-ish string from the VCS info we DO have. Better than
	// shipping `(devel)` to operators who'd ask "which build is this?".
	if out.Version == "" || out.Version == "(devel)" {
		if out.Commit != "" {
			short := out.Commit
			if len(short) > 7 {
				short = short[:7]
			}
			out.Version = "0.0.0-dev+" + short
			if out.Modified {
				out.Version += "-dirty"
			}
		} else {
			out.Version = "0.0.0-dev"
		}
	}
	return out
}

func init() {
	verifyCmd.Flags().BoolVar(&verifyJSON, "json", false, "Emit JSON instead of human output")
	verifyCmd.Flags().BoolVar(&verifyShowKey, "show-builtin-keys", false, "Include the minisign pubkey + keyid the binary was compiled with")
	rootCmd.AddCommand(verifyCmd)
}
