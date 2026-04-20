package main

// `stado session export` — render a session's conversation for
// sharing or archiving. Source is the worktree's
// `.stado/conversation.jsonl` (the same file the TUI replays on
// resume). Two output formats:
//
//   md    — readable markdown with role headers, code fences on
//           tool-use blocks, and a trailing metadata footer.
//   jsonl — the raw JSONL, copied verbatim. Useful for feeding
//           other tools without re-parsing the markdown.
//
// Dogfood context: today you have to cat the raw JSONL by hand and
// eyeball the block shapes. This command exists so sharing a
// session with a collaborator is `stado session export <id> -o
// session.md` + send the file.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/pkg/agent"
)

var (
	exportFormat string
	exportOutput string
)

var sessionExportCmd = &cobra.Command{
	Use:     "export <id>",
	Aliases: []string{"cat"},
	Short:   "Render a session's conversation as markdown (or raw JSONL)",
	Long: "Reads <worktree>/.stado/conversation.jsonl for the given session\n" +
		"and writes it out in a human-readable form. Default format is\n" +
		"markdown; --format jsonl emits the underlying append-only log\n" +
		"unchanged (suitable for piping into other tools).\n\n" +
		"Without --output, writes to stdout. Creates the parent dir if\n" +
		"the output path's dir doesn't exist yet.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		id := args[0]
		wt := filepath.Join(cfg.WorktreeDir(), id)
		if _, err := os.Stat(wt); err != nil {
			return fmt.Errorf("session export: session %s not found (no worktree at %s)", id, wt)
		}
		msgs, err := runtime.LoadConversation(wt)
		if err != nil {
			return fmt.Errorf("session export: %w", err)
		}
		if len(msgs) == 0 {
			return fmt.Errorf("session export: session %s has no persisted conversation (empty .stado/conversation.jsonl)", id)
		}

		var body []byte
		switch exportFormat {
		case "md", "":
			body = renderMarkdown(id, msgs)
		case "jsonl":
			raw, rerr := os.ReadFile(filepath.Join(wt, runtime.ConversationFile))
			if rerr != nil {
				return fmt.Errorf("session export: read jsonl: %w", rerr)
			}
			body = raw
		default:
			return fmt.Errorf("session export: unknown --format %q (want md|jsonl)", exportFormat)
		}

		if exportOutput == "" || exportOutput == "-" {
			_, err := os.Stdout.Write(body)
			return err
		}
		if dir := filepath.Dir(exportOutput); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("session export: mkdir %s: %w", dir, err)
			}
		}
		if err := os.WriteFile(exportOutput, body, 0o644); err != nil {
			return fmt.Errorf("session export: write %s: %w", exportOutput, err)
		}
		fmt.Fprintf(os.Stderr, "wrote %d message(s) to %s\n", len(msgs), exportOutput)
		return nil
	},
}

// renderMarkdown formats msgs as markdown with per-role headers and
// code-fenced tool-use / tool-result bodies. Keeps formatting simple
// enough that piping the output into `glow` or pasting into a PR
// description gives a readable transcript.
func renderMarkdown(sessionID string, msgs []agent.Message) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Session %s\n\n", sessionID)
	fmt.Fprintf(&b, "_%d message(s) exported from `.stado/conversation.jsonl`._\n\n", len(msgs))
	b.WriteString("---\n\n")
	for i, m := range msgs {
		writeMarkdownMessage(&b, i+1, m)
	}
	return []byte(b.String())
}

func writeMarkdownMessage(w io.Writer, n int, m agent.Message) {
	var role string
	switch m.Role {
	case agent.RoleUser:
		role = "User"
	case agent.RoleAssistant:
		role = "Assistant"
	case agent.RoleTool:
		role = "Tool result"
	default:
		role = string(m.Role)
	}
	fmt.Fprintf(w, "## %d. %s\n\n", n, role)

	for _, blk := range m.Content {
		switch {
		case blk.Text != nil:
			// Text blocks render as-is. Trim trailing whitespace so
			// consecutive messages don't accumulate blank lines.
			fmt.Fprintln(w, strings.TrimRight(blk.Text.Text, "\n"))
			fmt.Fprintln(w)
		case blk.Thinking != nil:
			fmt.Fprintln(w, "> _thinking_ (signature hidden)")
			fmt.Fprintln(w, ">")
			for _, line := range strings.Split(strings.TrimRight(blk.Thinking.Text, "\n"), "\n") {
				fmt.Fprintln(w, "> "+line)
			}
			fmt.Fprintln(w)
		case blk.ToolUse != nil:
			fmt.Fprintf(w, "**Tool call:** `%s`\n\n", blk.ToolUse.Name)
			fmt.Fprintln(w, "```json")
			if blk.ToolUse.Input != nil {
				fmt.Fprintln(w, string(blk.ToolUse.Input))
			}
			fmt.Fprintln(w, "```")
			fmt.Fprintln(w)
		case blk.ToolResult != nil:
			tag := "result"
			if blk.ToolResult.IsError {
				tag = "error"
			}
			fmt.Fprintf(w, "**Tool %s** (id: `%s`)\n\n", tag, blk.ToolResult.ToolUseID)
			fmt.Fprintln(w, "```")
			fmt.Fprintln(w, strings.TrimRight(blk.ToolResult.Content, "\n"))
			fmt.Fprintln(w, "```")
			fmt.Fprintln(w)
		case blk.Image != nil:
			fmt.Fprintln(w, "_[image omitted]_")
			fmt.Fprintln(w)
		}
	}
}

func init() {
	sessionExportCmd.Flags().StringVar(&exportFormat, "format", "md",
		"Output format: md (markdown, default) | jsonl (raw)")
	sessionExportCmd.Flags().StringVarP(&exportOutput, "output", "o", "",
		"Output path (default: stdout). Parent dir created if missing.")
	sessionCmd.AddCommand(sessionExportCmd)
}
