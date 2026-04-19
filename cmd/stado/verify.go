package main

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"

	"github.com/spf13/cobra"
)

var verifyJSON bool

var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Print build provenance + embedded trust info",
	Long: "Display the build info embedded at compile time:\n" +
		"- module version + VCS commit + VCS revision time\n" +
		"- Go toolchain version + target os/arch\n" +
		"- the binary's own mtime (when run from a release tarball, usually the\n" +
		"  release's build timestamp).\n\n" +
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
		return nil
	},
}

type buildInfo struct {
	Version    string `json:"version"`
	Commit     string `json:"commit"`
	CommitTime string `json:"commitTime"`
	Modified   bool   `json:"modified"`
	GoVersion  string `json:"goVersion"`
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
	MainModule string `json:"mainModule"`
}

func collectBuildInfo() buildInfo {
	out := buildInfo{
		Version:   version,
		GoVersion: runtime.Version(),
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
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
	return out
}

func init() {
	verifyCmd.Flags().BoolVar(&verifyJSON, "json", false, "Emit JSON instead of human output")
	rootCmd.AddCommand(verifyCmd)
}
