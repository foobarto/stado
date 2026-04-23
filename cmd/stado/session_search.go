package main

// `stado session search <query>` — grep across persisted
// conversations to answer "what did we decide about X last week?"
// Walks every session's .stado/conversation.jsonl, decodes each
// message's text blocks, returns matches with session id + message
// index + surrounding context.
//
// Case-insensitive substring match by default. -x / --regex
// switches to full regex (Go RE2).
//
// Why conversation.jsonl rather than the git trace ref: the trace
// ref strips content (see runtime/conversation.go's rationale —
// it's an audit log, not a transcript). conversation.jsonl IS the
// bytes the agent loop sees, so searching there mirrors "what did
// the user/assistant actually say?" faithfully.

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/textutil"
	"github.com/foobarto/stado/pkg/agent"
)

var (
	searchRegex   bool
	searchSession string
	searchMax     int
)

var sessionSearchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Grep conversation logs across every session (or just one via --session)",
	Long: "Walks every session's `.stado/conversation.jsonl` and returns the\n" +
		"messages whose text matches <query>. Case-insensitive substring by\n" +
		"default; --regex enables Go RE2 syntax. Use --session <id> to limit\n" +
		"to one session, --max N to cap total hits.\n\n" +
		"Matches include session id, role, message index, and a short excerpt\n" +
		"around the match so the output is scannable without `| less`.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := args[0]
		if query == "" {
			return fmt.Errorf("search: empty query")
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		var matcher func(string) bool
		if searchRegex {
			re, err := regexp.Compile("(?i)" + query)
			if err != nil {
				return fmt.Errorf("search: bad regex: %w", err)
			}
			matcher = re.MatchString
		} else {
			needle := strings.ToLower(query)
			matcher = func(s string) bool {
				return strings.Contains(strings.ToLower(s), needle)
			}
		}

		// Which sessions to scan.
		var ids []string
		if searchSession != "" {
			ids = []string{searchSession}
		} else {
			entries, err := os.ReadDir(cfg.WorktreeDir())
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Fprintln(os.Stderr, "(no worktrees — no sessions to search)")
					return nil
				}
				return fmt.Errorf("search: read worktrees: %w", err)
			}
			for _, e := range entries {
				if e.IsDir() {
					ids = append(ids, e.Name())
				}
			}
			sort.Strings(ids)
		}

		total := 0
		for _, id := range ids {
			hits, err := searchSessionConversation(cfg, id, matcher, searchMax-total)
			if err != nil {
				fmt.Fprintf(os.Stderr, "search: %s: %v\n", id, err)
				continue
			}
			for _, h := range hits {
				printMatch(id, h)
				total++
				if searchMax > 0 && total >= searchMax {
					fmt.Fprintf(os.Stderr, "(hit --max=%d, stopping)\n", searchMax)
					return nil
				}
			}
		}
		if total == 0 {
			fmt.Fprintln(os.Stderr, "(no matches)")
		} else {
			fmt.Fprintf(os.Stderr, "%d match(es) in %d session(s)\n", total, len(ids))
		}
		return nil
	},
}

// searchMatch is one hit: the session + message + role + an excerpt
// of the matched text for quick scanning.
type searchMatch struct {
	msgIndex int
	role     agent.Role
	excerpt  string
}

// searchSessionConversation loads one session's conversation and
// returns every message whose text content matches. remaining limits
// how many more matches the caller wants; 0 = unlimited.
func searchSessionConversation(cfg *config.Config, id string, match func(string) bool, remaining int) ([]searchMatch, error) {
	wt, err := worktreePathForID(cfg.WorktreeDir(), id)
	if err != nil {
		return nil, nil
	}
	if _, err := os.Stat(wt); err != nil {
		return nil, nil // detached session; nothing to search
	}
	msgs, err := runtime.LoadConversation(wt)
	if err != nil {
		return nil, err
	}
	var hits []searchMatch
	for i, m := range msgs {
		text := flattenMessageText(m)
		if text == "" {
			continue
		}
		if !match(text) {
			continue
		}
		hits = append(hits, searchMatch{
			msgIndex: i,
			role:     m.Role,
			excerpt:  excerptAround(text, match, 80),
		})
		if remaining > 0 && len(hits) >= remaining {
			break
		}
	}
	return hits, nil
}

// flattenMessageText concatenates every text-bearing block in a
// message for matching. Tool calls + results are included so the
// user can search for things they ran ("grep pattern") as well as
// what they asked.
func flattenMessageText(m agent.Message) string {
	var parts []string
	for _, b := range m.Content {
		switch {
		case b.Text != nil:
			parts = append(parts, b.Text.Text)
		case b.Thinking != nil:
			parts = append(parts, b.Thinking.Text)
		case b.ToolUse != nil:
			parts = append(parts, "[tool "+b.ToolUse.Name+"] "+string(b.ToolUse.Input))
		case b.ToolResult != nil:
			parts = append(parts, b.ToolResult.Content)
		}
	}
	return strings.Join(parts, "\n")
}

// excerptAround finds the first match and returns a snippet around
// it. Width caps the excerpt length — ellipses mark truncation on
// either side.
func excerptAround(text string, match func(string) bool, width int) string {
	lower := strings.ToLower(text)
	// Find the first contiguous chunk that matches. Cheap scan —
	// callers pass substring and regex matchers that yield true on
	// the first match anyway, so we don't need per-rune positioning.
	for _, line := range strings.Split(text, "\n") {
		if match(line) {
			return truncateAround(line, lower, width)
		}
	}
	return truncateAround(text, lower, width)
}

func truncateAround(s, lower string, width int) string {
	if len(s) <= width {
		return s
	}
	// Naive centre: cut a window around the middle. Good enough for
	// a scannable one-line excerpt.
	mid := len(s) / 2
	half := width / 2
	lo, hi := mid-half, mid+half
	if lo < 0 {
		lo = 0
	}
	if hi > len(s) {
		hi = len(s)
	}
	prefix, suffix := "", ""
	if lo > 0 {
		prefix = "…"
	}
	if hi < len(s) {
		suffix = "…"
	}
	return prefix + s[lo:hi] + suffix
}

func printMatch(id string, h searchMatch) {
	role := string(h.role)
	// Single space-separated line so `| grep` / `| awk` piping stays
	// practical — `session:id` prefix doubles as a columnar key.
	excerpt := strings.ReplaceAll(h.excerpt, "\n", " ")
	excerpt = textutil.StripControlChars(excerpt)
	fmt.Printf("session:%s msg:%d role:%s  %s\n", id, h.msgIndex, role, excerpt)
}

func init() {
	sessionSearchCmd.Flags().BoolVarP(&searchRegex, "regex", "x", false,
		"Interpret query as a Go RE2 regex (case-insensitive anchored)")
	sessionSearchCmd.Flags().StringVar(&searchSession, "session", "",
		"Restrict search to this session id")
	sessionSearchCmd.Flags().IntVar(&searchMax, "max", 0,
		"Cap total hits returned (0 = unlimited)")
}
