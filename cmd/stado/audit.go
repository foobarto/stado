package main

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/audit"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// refMaker is the function shape of TreeRef/TraceRef; used to iterate both.
type refMaker func(sessionID string) plumbing.ReferenceName

var auditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Verify and export stado's tamper-evident commit history",
}

var auditVerifyCmd = &cobra.Command{
	Use:   "verify [session-id]",
	Short: "Walk session refs and verify every commit's Ed25519 signature",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		priv, err := audit.LoadOrCreateKey(runtime.SigningKeyPath(cfg))
		if err != nil {
			return fmt.Errorf("audit: signing key: %w", err)
		}
		pub := priv.Public().(ed25519.PublicKey)

		sc, err := openSidecar(cfg)
		if err != nil {
			return err
		}
		ids := args
		if len(ids) == 0 {
			ids, err = listSessions(sc)
			if err != nil {
				return err
			}
		}
		if len(ids) == 0 {
			fmt.Fprintln(os.Stderr, "(no sessions)")
			return nil
		}

		allOK := true
		for _, id := range ids {
			for _, refPair := range []struct {
				name string
				ref  refMaker
			}{
				{"tree", stadogit.TreeRef},
				{"trace", stadogit.TraceRef},
			} {
				head, err := sc.ResolveRef(refPair.ref(id))
				if err != nil {
					continue // ref may not exist yet
				}
				w := audit.NewWalker(sc.Repo().Storer, pub)
				res, err := w.Verify(string(refPair.ref(id)), head)
				if err != nil {
					return err
				}
				status := "OK"
				if res.Invalid > 0 {
					status = "FAIL"
					allOK = false
				} else if res.Unsigned > 0 {
					status = "UNSIGNED"
					allOK = false
				}
				fmt.Printf("%s\t%s\t%s\t%d total (%d signed, %d unsigned, %d invalid)\n",
					status, id, refPair.name,
					res.TotalCommits, res.Signed, res.Unsigned, res.Invalid)
				if res.InvalidAt.IsZero() && res.FirstUnsignedAt.IsZero() {
					continue
				}
				if !res.InvalidAt.IsZero() {
					fmt.Fprintf(os.Stderr, "  first invalid at: %s\n", res.InvalidAt)
				}
				if !res.FirstUnsignedAt.IsZero() {
					fmt.Fprintf(os.Stderr, "  first unsigned at: %s\n", res.FirstUnsignedAt)
				}
			}
		}
		if !allOK {
			os.Exit(1)
		}
		return nil
	},
}

var auditExportCmd = &cobra.Command{
	Use:   "export [session-id]",
	Short: "Emit tree/trace commits as JSON lines for SIEM ingestion",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		sc, err := openSidecar(cfg)
		if err != nil {
			return err
		}
		ids := args
		if len(ids) == 0 {
			ids, err = listSessions(sc)
			if err != nil {
				return err
			}
		}
		for _, id := range ids {
			for _, refPair := range []struct {
				name string
				ref  refMaker
			}{
				{"tree", stadogit.TreeRef},
				{"trace", stadogit.TraceRef},
			} {
				head, err := sc.ResolveRef(refPair.ref(id))
				if err != nil {
					continue
				}
				if err := audit.ExportJSONL(os.Stdout, sc.Repo().Storer, string(refPair.ref(id)), head); err != nil {
					return err
				}
			}
		}
		return nil
	},
}

var auditPubkeyCmd = &cobra.Command{
	Use:   "pubkey",
	Short: "Print the agent signing public key + fingerprint",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		priv, err := audit.LoadOrCreateKey(runtime.SigningKeyPath(cfg))
		if err != nil {
			return err
		}
		pub := priv.Public().(ed25519.PublicKey)
		fmt.Printf("%s  %s\n", audit.Fingerprint(pub), hexString(pub))
		return nil
	},
}

func init() {
	auditCmd.AddCommand(auditVerifyCmd, auditExportCmd, auditPubkeyCmd)
	rootCmd.AddCommand(auditCmd)
}

// hexString avoids hard dep on encoding/hex at the top of the file.
func hexString(b []byte) string {
	var sb strings.Builder
	for _, x := range b {
		const digits = "0123456789abcdef"
		sb.WriteByte(digits[x>>4])
		sb.WriteByte(digits[x&0xf])
	}
	return sb.String()
}
