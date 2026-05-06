package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/secrets"
)

// openSecretStore loads config and returns a secrets.Store rooted at the
// configured state dir.
func openSecretStore() (*secrets.Store, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return secrets.NewStore(cfg.StateDir()), nil
}

var secretsCmd = &cobra.Command{
	Use:   "secrets",
	Short: "Manage operator secrets (API tokens, passwords, etc.)",
	Long: "Manage the operator secret store at <state-dir>/secrets/.\n\n" +
		"Secrets are stored as files with mode 0600. Plugins access them\n" +
		"via stado_secrets_get/put host imports; the operator provisions\n" +
		"them via this CLI. Values are never written to logs or audit trails.",
}

// secretsFromFile is the --from-file flag value for `secrets set`.
var secretsFromFile string

var secretsSetCmd = &cobra.Command{
	Use:   "set <name>",
	Short: "Set a secret (reads from stdin by default)",
	Long: "Read the secret value from stdin (default), --from-stdin, or\n" +
		"--from-file=<path>, then store it under <name>.\n\n" +
		"The value is stored raw (no trailing newline is stripped). If you\n" +
		"pipe from `echo`, the newline becomes part of the stored value;\n" +
		"use `printf` or `echo -n` if that matters.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := secrets.ValidName(name); err != nil {
			return err
		}

		var value []byte
		var err error
		if secretsFromFile != "" {
			value, err = os.ReadFile(secretsFromFile)
			if err != nil {
				return fmt.Errorf("secrets set: read file: %w", err)
			}
		} else {
			// Default: read from stdin (matches --from-stdin behaviour).
			value, err = io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("secrets set: read stdin: %w", err)
			}
		}

		store, err := openSecretStore()
		if err != nil {
			return err
		}
		if err := store.Put(name, value); err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "secret %q stored\n", name)
		return nil
	},
}

var secretsGetCmd = &cobra.Command{
	Use:   "get <name>",
	Short: "Get a secret and write its raw bytes to stdout",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := secrets.ValidName(name); err != nil {
			return err
		}
		store, err := openSecretStore()
		if err != nil {
			return err
		}
		value, err := store.Get(name)
		if err != nil {
			if errors.Is(err, secrets.ErrNotFound) {
				fmt.Fprintf(cmd.ErrOrStderr(), "secrets: %q not found\n", name)
				return fmt.Errorf("secret %q not found", name)
			}
			return err
		}
		_, err = cmd.OutOrStdout().Write(value)
		return err
	},
}

var secretsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all secret names (one per line)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := openSecretStore()
		if err != nil {
			return err
		}
		names, err := store.List()
		if err != nil {
			return err
		}
		if len(names) == 0 {
			fmt.Fprintln(cmd.ErrOrStderr(), "(no secrets)")
			return nil
		}
		fmt.Fprintln(cmd.OutOrStdout(), strings.Join(names, "\n"))
		return nil
	},
}

var secretsRmCmd = &cobra.Command{
	Use:   "rm <name>",
	Short: "Remove a secret",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := secrets.ValidName(name); err != nil {
			return err
		}
		store, err := openSecretStore()
		if err != nil {
			return err
		}
		if err := store.Remove(name); err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "secret %q removed\n", name)
		return nil
	},
}

func init() {
	secretsSetCmd.Flags().StringVar(&secretsFromFile, "from-file", "",
		"Read secret value from this file path instead of stdin")
	secretsSetCmd.Flags().Bool("from-stdin", false,
		"Explicitly read secret value from stdin (default behaviour)")

	secretsCmd.AddCommand(secretsSetCmd, secretsGetCmd, secretsListCmd, secretsRmCmd)
}
