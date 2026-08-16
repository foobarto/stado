package main

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/runtime"
)

var pluginABICheckCmd = &cobra.Command{
	Use:   "abi-check <plugin-dir> [plugin-dir...]",
	Short: "Check plugin WASM imports and exports against this stado host ABI",
	Long: "Compiles each digest-verified plugin without executing guest code and compares\n" +
		"every stado host import plus every manifest-required export by exact WebAssembly\n" +
		"function signature. This is a compatibility check, not signer authentication; use\n" +
		"`stado plugin verify` when admitting a package.",
	Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPluginABICheck(cmd.Context(), cmd.OutOrStdout(), args)
	},
}

func runPluginABICheck(ctx context.Context, out io.Writer, directories []string) error {
	for _, directory := range directories {
		issue, err := runtime.CheckPluginPackageABI(ctx, directory)
		if err != nil {
			return fmt.Errorf("ABI check %s: %w", directory, err)
		}
		if issue.HasProblems() {
			return fmt.Errorf("ABI check %s: %s", directory, issue.String())
		}
		fmt.Fprintf(out, "OK  %s v%s  ABI-compatible\n", issue.Plugin, issue.Version)
	}
	return nil
}
