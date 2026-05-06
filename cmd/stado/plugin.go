package main

import (
	"github.com/spf13/cobra"
)

var pluginCmd = &cobra.Command{
	Use:   "plugin",
	Short: "Manage trusted plugin signers + verify plugin packages",
	Long: "stado's plugin model: every plugin ships a signed manifest. Before it\n" +
		"can run, the author's Ed25519 public key must be pinned via\n" +
		"`stado plugin trust`, and the signature + wasm sha256 + minimum\n" +
		"stado-version + rollback protection are all checked by\n" +
		"`stado plugin verify`. `stado plugin install` copies a verified\n" +
		"plugin into stado's state dir, after which `stado tool run <name>`\n" +
		"(or `/plugin:<name>-<ver>` in the TUI) can invoke its declared\n" +
		"tools via the wazero wasm runtime.",
}

func init() {
	pluginInstallCmd.Flags().StringVar(&pluginInstallSigner, "signer", "",
		"Pin the plugin's author Ed25519 pubkey (hex or base64) inline before verification. Only use when you've verified the signer out of band.")
	pluginInstallCmd.Flags().BoolVar(&pluginInstallForce, "force", false,
		"Force reinstall even when the same version is already present (bypasses idempotency check). EP-0039.")
	pluginInstallCmd.Flags().BoolVar(&pluginInstallAutoload, "autoload", false,
		"After install, persist the plugin's tools into [tools].autoload in config.toml — they'll load into every session without a separate `stado tool autoload` call.")
	pluginTrustCmd.Flags().StringVar(&pluginTrustPubkeyFile, "pubkey-file", "",
		"Path to a file containing the hex-encoded Ed25519 public key (alternative to passing inline). EP-0039.")
	pluginSignCmd.Flags().StringVar(&pluginSignKeyPath, "key", "",
		"Path to the 32-byte Ed25519 seed (generate via `stado plugin gen-key`). Mutually exclusive with --key-env.")
	pluginSignCmd.Flags().StringVar(&pluginSignKeyEnv, "key-env", "",
		"Name of an env var holding the Ed25519 seed in hex (64 chars) or base64 (44 chars). Designed for CI: secrets injected at job time without writing them to disk. Mutually exclusive with --key.")
	pluginSignCmd.Flags().StringVar(&pluginSignWasm, "wasm", "",
		"Path to the plugin wasm binary (default: <manifest-dir>/plugin.wasm)")
	pluginSignCmd.Flags().StringVar(&pluginSignManifestVersion, "manifest-version", "",
		"Override the version field in the manifest before signing (used by `plugin dev --watch`)")
	pluginSignCmd.Flags().BoolVar(&pluginSignQuiet, "quiet", false,
		"Suppress informational stdout (CI mode). Errors still go to stderr.")
	pluginCmd.AddCommand(pluginTrustCmd, pluginUntrustCmd, pluginListCmd, pluginInstalledCmd, pluginVerifyCmd,
		pluginDigestCmd, pluginInstallCmd, pluginGenKeyCmd, pluginSignCmd,
		pluginGCCmd, pluginDoctorCmd, pluginInfoCmd, pluginReloadCmd,
		// EP-0039: distribution and trust additions.
		pluginUseCmd, pluginDevCmd)
	rootCmd.AddCommand(pluginCmd)
}
