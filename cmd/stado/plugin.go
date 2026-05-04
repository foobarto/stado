package main

import (
	"github.com/spf13/cobra"
)

var pluginCmd = &cobra.Command{
	Use:   "plugin",
	Short: "Manage trusted plugin signers + verify + run plugin packages",
	Long: "stado's plugin model: every plugin ships a signed manifest. Before it\n" +
		"can run, the author's Ed25519 public key must be pinned via\n" +
		"`stado plugin trust`, and the signature + wasm sha256 + minimum\n" +
		"stado-version + rollback protection are all checked by\n" +
		"`stado plugin verify`. `stado plugin install` copies a verified\n" +
		"plugin into stado's state dir, after which `stado plugin run` (or\n" +
		"`/plugin:<name>-<ver>` in the TUI) can invoke its declared tools\n" +
		"via the wazero wasm runtime.",
}

func init() {
	pluginInstallCmd.Flags().StringVar(&pluginInstallSigner, "signer", "",
		"Pin the plugin's author Ed25519 pubkey (hex or base64) inline before verification. Only use when you've verified the signer out of band.")
	pluginSignCmd.Flags().StringVar(&pluginSignKeyPath, "key", "",
		"Path to the 32-byte Ed25519 seed (generate via `stado plugin gen-key`)")
	pluginSignCmd.Flags().StringVar(&pluginSignWasm, "wasm", "",
		"Path to the plugin wasm binary (default: <manifest-dir>/plugin.wasm)")
	pluginCmd.AddCommand(pluginTrustCmd, pluginUntrustCmd, pluginListCmd, pluginInstalledCmd, pluginVerifyCmd,
		pluginDigestCmd, pluginInstallCmd, pluginRunCmd, pluginGenKeyCmd, pluginSignCmd,
		pluginGCCmd)
	rootCmd.AddCommand(pluginCmd)
}
