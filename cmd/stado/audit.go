package main

import (
	"crypto/ed25519"
	"errors"
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

		// B8: an explicitly-named id that resolves no refs must error rather
		// than silently exit 0. The no-args sweep keeps the lenient skip.
		explicitID := len(args) > 0
		allOK := true
		brokenLinks := false
		for _, id := range ids {
			found := false
			for _, refPair := range []struct {
				name string
				ref  refMaker
			}{
				{"tree", stadogit.TreeRef},
				{"trace", stadogit.TraceRef},
			} {
				head, err := sc.ResolveRef(refPair.ref(id))
				if err != nil {
					if !errors.Is(err, plumbing.ErrReferenceNotFound) {
						return fmt.Errorf("audit verify: resolve %s: %w", refPair.ref(id), err)
					}
					continue // ref legitimately doesn't exist yet
				}
				found = true
				w := audit.NewWalker(sc.Repo().Storer, pub)
				res, err := w.Verify(string(refPair.ref(id)), head)
				if err != nil {
					return err
				}
				status := "OK"
				switch {
				case res.Invalid > 0:
					status = "FAIL"
					allOK = false
				case res.LegacyV1 > 0:
					// Distinct from FAIL: not tampered, just pre-v2 scheme that
					// is no longer accepted. Still not verified, so non-OK.
					status = "LEGACY-V1"
					allOK = false
				case res.Unsigned > 0:
					status = "UNSIGNED"
					allOK = false
				}
				fmt.Printf("%s\t%s\t%s\t%d total (%d signed, %d unsigned, %d invalid, %d legacy-v1)\n",
					status, id, refPair.name,
					res.TotalCommits, res.Signed, res.Unsigned, res.Invalid, res.LegacyV1)
				if !res.InvalidAt.IsZero() {
					fmt.Fprintf(os.Stderr, "  first invalid at: %s\n", res.InvalidAt)
				}
				if !res.FirstLegacyV1At.IsZero() {
					fmt.Fprintf(os.Stderr, "  first legacy-v1 at: %s  (pre-v2 scheme; re-sign to verify under v2)\n", res.FirstLegacyV1At)
				}
				if !res.FirstUnsignedAt.IsZero() {
					fmt.Fprintf(os.Stderr, "  first unsigned at: %s\n", res.FirstUnsignedAt)
				}
				// Hook-mutation provenance chains. Print one line per
				// mutation link; flag broken links as a DISTINCT anomaly
				// class from signature failure (a broken link does NOT mean
				// the signatures failed — both commits can be validly
				// signed while their content linkage was tampered).
				for _, link := range res.MutationChain {
					backing := "sha-only"
					if link.BlobBacked {
						backing = "blob"
					}
					if link.Broken {
						brokenLinks = true
						fmt.Printf("  MUTATION-LINK-BROKEN\t%s @ %s\tby %s\t%s -> %s\t(%s)\n",
							orDash(link.Tool), shortHash(link.Commit), orDash(link.ByHook),
							orDash(link.OriginalSHA), orDash(link.MutatedSHA), backing)
						fmt.Fprintf(os.Stderr, "    broken: %s\n", link.BrokenReason)
					} else {
						fmt.Printf("  mutation-link\t%s @ %s\tby %s\t%s -> %s\t(%s)\n",
							orDash(link.Tool), shortHash(link.Commit), orDash(link.ByHook),
							orDash(link.OriginalSHA), orDash(link.MutatedSHA), backing)
					}
				}
			}
			if !found && explicitID {
				return fmt.Errorf("audit verify: session %s not found (no tree/trace refs)", id)
			}
		}
		// Non-zero exit on a signature failure / unsigned commit OR a broken
		// mutation link. A mutation link that's merely PRESENT (intact) is
		// reported but does not affect the exit code.
		if !allOK || brokenLinks {
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
		// B8 sibling: an explicitly-named id that resolves no refs must
		// error rather than silently exit 0 with empty output. For a
		// SIEM-ingestion tool, a typoed/nonexistent id producing zero
		// records under a success code is silent data-loss. The no-args
		// sweep keeps the lenient skip (nothing to export → exit 0).
		explicitID := len(args) > 0
		for _, id := range ids {
			found := false
			for _, refPair := range []struct {
				name string
				ref  refMaker
			}{
				{"tree", stadogit.TreeRef},
				{"trace", stadogit.TraceRef},
			} {
				head, err := sc.ResolveRef(refPair.ref(id))
				if err != nil {
					// Mirror `audit verify`: a genuine storage error must
					// surface, not be misreported as a missing session. Only
					// a legitimately-absent ref is the benign skip case.
					if !errors.Is(err, plumbing.ErrReferenceNotFound) {
						return fmt.Errorf("audit export: resolve %s: %w", refPair.ref(id), err)
					}
					continue // ref legitimately doesn't exist yet
				}
				found = true
				if err := audit.ExportJSONL(os.Stdout, sc.Repo().Storer, string(refPair.ref(id)), head); err != nil {
					return err
				}
			}
			if !found && explicitID {
				return fmt.Errorf("audit export: session %s not found (no tree/trace refs)", id)
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

// orDash renders an empty string as "-" so a tab-separated mutation-link line
// keeps its column alignment when a trailer is absent.
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
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
