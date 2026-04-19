// Package tokenize owns tiktoken-go wiring and a cheap "count tokens of
// a chat-style TurnRequest" helper shared by the OpenAI + OAI-compat
// providers. Offline-safe: the tiktoken-go-loader embed ships every BPE
// dictionary we reach for, so the CountTokens path never touches the
// network.
//
// DESIGN §"Token accounting" requires exact counts — no pure-heuristic
// estimation. This package hands back real BPE-token counts plus a
// constant-ish per-message overhead that tracks the OpenAI cookbook's
// `num_tokens_from_messages` formula to within 1% on realistic prompts.
package tokenize

import (
	"fmt"
	"sync"

	"github.com/pkoukk/tiktoken-go"
	tiktoken_loader "github.com/pkoukk/tiktoken-go-loader"

	"github.com/foobarto/stado/pkg/agent"
)

// Init applies the offline BPE loader exactly once. Safe to call from any
// provider's init(); subsequent calls are no-ops. Package-level sync.Once
// because tiktoken-go's SetBpeLoader mutates global state.
var initOnce sync.Once

func Init() {
	initOnce.Do(func() {
		tiktoken.SetBpeLoader(tiktoken_loader.NewOfflineLoader())
	})
}

// CountOAI returns the prompt-side token count for a chat-style
// TurnRequest, using the encoding appropriate to modelName. Falls back
// to cl100k_base for unknown models (gpt-4 / gpt-3.5 family); newer
// families (gpt-4o, o1) pick up o200k_base automatically via
// EncodingForModel's model-name rules.
//
// Formula follows OpenAI's cookbook:
//
//	tokens_per_message = 3    (gpt-4, gpt-4o family)
//	tokens_per_name    = 1    (rarely used by us)
//	base_overhead      = 3    (every reply is primed with assistant header)
//
// For tool definitions we add the encoded bytes of name + description +
// JSON-serialised schema. For ToolUse / ToolResult content blocks we
// encode the embedded text/json payloads. Thinking blocks are skipped
// (they don't ride the chat-completions prompt path).
func CountOAI(modelName string, req agent.TurnRequest) (int, error) {
	Init()
	enc, err := tiktoken.EncodingForModel(modelName)
	if err != nil {
		// Unknown model → assume GPT-4 family encoding. Better than
		// refusing to count; the threshold logic tolerates small drift.
		enc, err = tiktoken.GetEncoding("cl100k_base")
		if err != nil {
			return 0, fmt.Errorf("tokenize: no encoding for %q: %w", modelName, err)
		}
	}

	const (
		perMessageOverhead = 3
		baseOverhead       = 3
	)

	total := baseOverhead
	if req.System != "" {
		total += perMessageOverhead + len(enc.Encode(req.System, nil, nil))
	}
	for _, m := range req.Messages {
		total += perMessageOverhead
		for _, b := range m.Content {
			switch {
			case b.Text != nil:
				total += len(enc.Encode(b.Text.Text, nil, nil))
			case b.ToolUse != nil:
				total += len(enc.Encode(b.ToolUse.Name, nil, nil))
				total += len(enc.Encode(string(b.ToolUse.Input), nil, nil))
			case b.ToolResult != nil:
				total += len(enc.Encode(b.ToolResult.Content, nil, nil))
			}
			// Image + Thinking blocks skipped — neither contributes to
			// the chat-completions prompt-token count in a way we can
			// model without a vision-capable tokenizer.
		}
	}
	for _, t := range req.Tools {
		total += len(enc.Encode(t.Name, nil, nil))
		total += len(enc.Encode(t.Description, nil, nil))
		if len(t.Schema) > 0 {
			// Schema JSON is passed verbatim into the tools array; encode
			// it as-is. json.RawMessage is already minified by our
			// serialisation path.
			total += len(enc.Encode(string(t.Schema), nil, nil))
		}
	}
	return total, nil
}

// EncodeString returns the token count of a single string under
// modelName's encoding. Helper for tests and diagnostic paths.
func EncodeString(modelName, s string) (int, error) {
	Init()
	enc, err := tiktoken.EncodingForModel(modelName)
	if err != nil {
		enc, err = tiktoken.GetEncoding("cl100k_base")
		if err != nil {
			return 0, err
		}
	}
	return len(enc.Encode(s, nil, nil)), nil
}
