// Package compact implements user-invoked conversation compaction
// (PLAN §11.3, DESIGN §"Compaction").
//
// Compaction is strictly user-invoked — there is no automatic summariser,
// no background compactor, no threshold-triggered eviction. Forking from
// an earlier turn (see `session tree` / `session fork --at`) is the
// preferred recovery for oversized sessions; this package exists for the
// occasional case where forking is unworkable and the user consciously
// chooses a lossy summary over a context-window refusal.
//
// The summarisation prompt is intentionally conservative: it asks the
// model to preserve file paths, identifiers, decisions, and open
// questions verbatim, and to drop conversational filler. The resulting
// summary becomes the first message in the post-compaction conversation;
// the user can continue from there.
package compact

import (
	"context"
	"fmt"
	"strings"

	"github.com/foobarto/stado/pkg/agent"
)

// SystemPrompt is the summarisation instruction fed to the compaction
// turn. Kept public so tests can pin its wording — a spec-guided string
// is part of the feature, not an implementation detail.
const SystemPrompt = `You are compacting a coding-agent conversation into a concise summary.

Preserve VERBATIM: file paths, identifier names, function signatures,
configuration values, and any unresolved questions the user raised.

Keep: decisions reached, open problems, and the next action the user or
the agent is expected to take.

Drop: filler, step-by-step reasoning that reached a conclusion already
captured above, verbose tool-call output that's already been acted on.

Output a single markdown block. No preamble, no "here's a summary"
sentence — start directly with the content. Aim for 400 tokens or less.`

// BuildRequest prepares the summarisation turn for a given conversation
// history. The returned TurnRequest is ready to hand to
// Provider.StreamTurn; the caller is expected to render the streaming
// result to the user for approval before replacing the live msgs list.
func BuildRequest(model string, msgs []agent.Message) agent.TurnRequest {
	userInstruction := renderConversation(msgs)
	return agent.TurnRequest{
		Model:  model,
		System: SystemPrompt,
		Messages: []agent.Message{
			agent.Text(agent.RoleUser, userInstruction),
		},
		// No tools on a summarisation call — we want text back, not a
		// plan that calls out to tools.
		Tools: nil,
	}
}

// renderConversation flattens msgs into a plain-text transcript the
// summariser can read. Tool-uses and tool-results appear as inline
// markers so the summariser can reference them by tool name.
func renderConversation(msgs []agent.Message) string {
	var b strings.Builder
	b.WriteString("Conversation so far:\n\n")
	for _, m := range msgs {
		switch m.Role {
		case agent.RoleUser:
			b.WriteString("--- USER ---\n")
		case agent.RoleAssistant:
			b.WriteString("--- ASSISTANT ---\n")
		case agent.RoleTool:
			b.WriteString("--- TOOL RESULTS ---\n")
		}
		for _, blk := range m.Content {
			switch {
			case blk.Text != nil:
				b.WriteString(blk.Text.Text)
				b.WriteString("\n")
			case blk.ToolUse != nil:
				fmt.Fprintf(&b, "[tool_use %s: %s]\n", blk.ToolUse.Name, truncate(string(blk.ToolUse.Input), 200))
			case blk.ToolResult != nil:
				fmt.Fprintf(&b, "[tool_result %s]\n%s\n",
					blk.ToolResult.ToolUseID, truncate(blk.ToolResult.Content, 600))
			}
		}
		b.WriteString("\n")
	}
	b.WriteString(
		"Produce the compacted summary now, following the instructions in the system prompt.")
	return b.String()
}

// Summarise is a convenience helper for callers that want a synchronous
// "text in, summary out" interface. Drives the streaming turn and
// collects all text deltas. Returns early on EvError.
func Summarise(ctx context.Context, p agent.Provider, model string, msgs []agent.Message) (string, error) {
	ch, err := p.StreamTurn(ctx, BuildRequest(model, msgs))
	if err != nil {
		return "", fmt.Errorf("compact: stream: %w", err)
	}
	var sb strings.Builder
	for ev := range ch {
		switch ev.Kind {
		case agent.EvTextDelta:
			sb.WriteString(ev.Text)
		case agent.EvError:
			return sb.String(), fmt.Errorf("compact: %w", ev.Err)
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

// ReplaceMessages builds the post-compaction message list: one
// user-role Message containing the summary, for the model to see as the
// earliest context on the next turn. Callers typically pass this to the
// next Provider.StreamTurn invocation and discard the prior msgs.
//
// Kept as a named helper so the exact shape is pinned against
// DESIGN §"Compaction"'s invariant: the summary replaces the
// conversation-view while trace ref history is untouched. Tests pin
// the shape; behaviour changes here surface as test failures.
func ReplaceMessages(summary string) []agent.Message {
	label := "[compaction summary — prior turns compacted by user]\n\n" + summary
	return []agent.Message{agent.Text(agent.RoleUser, label)}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
