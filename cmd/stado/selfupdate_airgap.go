//go:build airgap

package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// selfUpdateCmd in airgap builds refuses to run — there's no network
// egress in this build. Operators get a useful hint pointing at the
// air-gapped install path (download + `stado verify <artifact>` on
// another machine, then copy the binary over).
var selfUpdateCmd = &cobra.Command{
	Use:   "self-update",
	Short: "Disabled in airgap builds (-tags airgap)",
	Long: "stado was built with -tags airgap, which strips every network\n" +
		"call from the binary. `self-update` is one of those — in airgap\n" +
		"mode, upgrade by:\n" +
		"  1. downloading the new release on an online host\n" +
		"  2. verifying it with `stado verify <artifact>` (minisign)\n" +
		"  3. copying the binary into place on this host.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("self-update disabled in airgap build; see `stado self-update --help`")
	},
}

func init() {
	rootCmd.AddCommand(selfUpdateCmd)
}
