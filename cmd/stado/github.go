package main

// `stado github install` — writes a GitHub Actions workflow that runs
// stado against the repo when someone comments "@stado <instruction>"
// on an issue or PR. Mirrors the pattern opencode (sst/opencode) and
// Claude Code's GitHub app ship — brings stado's sandbox + signed-
// release story to the "agent touches your repo" case.
//
// The install command only writes the workflow template. Users still
// have to:
//   1. Commit the workflow
//   2. Set GITHUB_TOKEN (automatic) + ANTHROPIC_API_KEY (or other
//      provider key) as repo secrets
//   3. Pick a model via STADO_DEFAULTS_MODEL env override or by
//      editing the workflow directly
//
// Dogfood-gap #3 from the opencode/pi research.

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	githubWorkflowPath string
	githubForce        bool
)

var githubCmd = &cobra.Command{
	Use:   "github",
	Short: "Install + manage the GitHub Actions workflow that runs stado on @mentions",
}

var githubInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Write the stado-bot workflow to .github/workflows/",
	Long: "Generates a GitHub Actions workflow that runs `stado run --prompt`\n" +
		"inside a sandboxed Actions runner when someone comments `@stado <text>`\n" +
		"on an issue or PR. The comment body (minus the @stado trigger) becomes\n" +
		"the prompt; stado's reply is posted back as a follow-up comment.\n\n" +
		"Required repository secrets:\n" +
		"  - ANTHROPIC_API_KEY (or OPENAI_API_KEY / GEMINI_API_KEY — pick\n" +
		"    whichever provider you want and edit the workflow accordingly)\n\n" +
		"Default output: .github/workflows/stado-bot.yml. Override with --path.\n" +
		"Existing files are not overwritten unless --force is passed.",
	RunE: func(cmd *cobra.Command, args []string) error {
		dest := githubWorkflowPath
		if dest == "" {
			dest = filepath.Join(".github", "workflows", "stado-bot.yml")
		}
		if _, err := os.Stat(dest); err == nil && !githubForce {
			return fmt.Errorf("%s already exists (use --force to overwrite)", dest)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", filepath.Dir(dest), err)
		}
		if err := os.WriteFile(dest, []byte(githubBotWorkflow), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", dest)
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "Next steps:")
		fmt.Fprintln(os.Stderr, "  1. Review the workflow — model id + provider")
		fmt.Fprintln(os.Stderr, "     defaults are placeholder values.")
		fmt.Fprintln(os.Stderr, "  2. Add `ANTHROPIC_API_KEY` (or equivalent) to the")
		fmt.Fprintln(os.Stderr, "     repo's Actions secrets.")
		fmt.Fprintln(os.Stderr, "  3. Commit + push. Comment `@stado <instruction>` on")
		fmt.Fprintln(os.Stderr, "     an issue or PR to trigger.")
		return nil
	},
}

var githubUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove the stado-bot workflow file",
	RunE: func(cmd *cobra.Command, args []string) error {
		dest := githubWorkflowPath
		if dest == "" {
			dest = filepath.Join(".github", "workflows", "stado-bot.yml")
		}
		if _, err := os.Stat(dest); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "%s does not exist — nothing to do\n", dest)
			return nil
		}
		if err := os.Remove(dest); err != nil {
			return fmt.Errorf("remove %s: %w", dest, err)
		}
		fmt.Fprintf(os.Stderr, "removed %s\n", dest)
		fmt.Fprintln(os.Stderr, "remember to commit the deletion + push to deactivate.")
		return nil
	},
}

// githubBotWorkflow is the template written by `stado github install`.
// Keeps the model id + provider in a single top-level `env:` block so
// a user can swap providers with one edit. Uses `gh` (preinstalled on
// GitHub runners) to post the reply back as an issue/PR comment.
//
// The `fetch-stado` step downloads a specific release — pinning to a
// tag + sha256 would be a hardening follow-up once a stable release
// exists. For now it uses `latest`.
const githubBotWorkflow = `name: stado-bot

on:
  issue_comment:
    types: [created]
  pull_request_review_comment:
    types: [created]

permissions:
  contents: read
  issues: write
  pull-requests: write

jobs:
  trigger:
    if: >-
      ${{
        startsWith(github.event.comment.body, '@stado ') &&
        contains(fromJson('["OWNER","MEMBER","COLLABORATOR"]'), github.event.comment.author_association)
      }}
    runs-on: ubuntu-latest
    env:
      # Edit these two to swap provider/model. STADO_DEFAULTS_* env vars
      # override config.toml (which doesn't exist in the runner anyway).
      STADO_DEFAULTS_PROVIDER: anthropic
      STADO_DEFAULTS_MODEL: claude-sonnet-4-6
      ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
      GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}
    steps:
      - name: Checkout
        uses: actions/checkout@v4
        with:
          fetch-depth: 0 # stado's sidecar wants full history for session/fork primitives

      - name: Fetch stado
        run: |
          set -euo pipefail
          # Pin a release tag when cutting production use. 'latest' works for
          # kicking tires but doesn't give you SLSA-verifiable provenance.
          gh release download --repo foobarto/stado --pattern 'stado_*linux_amd64.tar.gz' --dir /tmp/stado
          tar -xzf /tmp/stado/stado_*linux_amd64.tar.gz -C /tmp/stado
          chmod +x /tmp/stado/stado
          /tmp/stado/stado version

      - name: Extract instruction
        id: extract
        env:
          BODY: ${{ github.event.comment.body }}
        run: |
          # Strip leading "@stado " and any trailing whitespace. The rest
          # is the prompt. Preserve newlines; GitHub passes them through
          # as literal \n which echo handles natively.
          echo "prompt<<STADO_EOF" >> $GITHUB_OUTPUT
          echo "$BODY" | sed -E 's/^@stado[[:space:]]+//' >> $GITHUB_OUTPUT
          echo "STADO_EOF" >> $GITHUB_OUTPUT

      - name: Run stado
        id: run
        run: |
          set -euo pipefail
          output=$(/tmp/stado/stado run --prompt "${{ steps.extract.outputs.prompt }}" --tools 2>&1 || true)
          echo "reply<<STADO_EOF" >> $GITHUB_OUTPUT
          echo "$output" >> $GITHUB_OUTPUT
          echo "STADO_EOF" >> $GITHUB_OUTPUT

      - name: Post reply
        run: |
          reply=$(cat <<'REPLY_EOF'
          **stado** (model: ${STADO_DEFAULTS_MODEL}):

          ${{ steps.run.outputs.reply }}

          ---
          _Triggered by @${{ github.event.comment.user.login }} — see the [action run](${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}) for the full trace._
          REPLY_EOF
          )
          gh api \
            --method POST \
            -H "Accept: application/vnd.github+json" \
            /repos/${{ github.repository }}/issues/${{ github.event.issue.number || github.event.pull_request.number }}/comments \
            -f body="$reply"
`

func init() {
	githubInstallCmd.Flags().StringVar(&githubWorkflowPath, "path", "",
		"Destination workflow file (default: .github/workflows/stado-bot.yml)")
	githubInstallCmd.Flags().BoolVar(&githubForce, "force", false,
		"Overwrite an existing file at the destination")
	githubUninstallCmd.Flags().StringVar(&githubWorkflowPath, "path", "",
		"Workflow file to remove (default: .github/workflows/stado-bot.yml)")
	githubCmd.AddCommand(githubInstallCmd, githubUninstallCmd)
	rootCmd.AddCommand(githubCmd)
}
