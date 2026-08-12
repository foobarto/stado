package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/pkg/agent"
)

// SessionCorpus is constructed by the broker from already-authorized session
// roots. A query cannot add a path/session ID that was not in this map.
type SessionCorpus struct {
	Authorized  map[string]string
	MaxMessages int
}

func (s SessionCorpus) Catalog(ctx context.Context, max int) ([]CatalogItem, error) {
	_ = ctx
	ids := make([]string, 0, len(s.Authorized))
	for id := range s.Authorized {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > max {
		ids = ids[:max]
	}
	out := make([]CatalogItem, 0, len(ids))
	for _, id := range ids {
		msgs, err := runtime.LoadConversation(s.Authorized[id])
		if err != nil {
			continue
		}
		digest := conversationDigest(msgs)
		out = append(out, CatalogItem{Ref: Ref{Kind: "session", ID: id, Locator: "conversation", Digest: digest}, Summary: fmt.Sprintf("session %s (%d messages)", id, len(msgs))})
	}
	return out, nil
}
func (s SessionCorpus) Search(ctx context.Context, q string, max int) ([]CatalogItem, error) {
	_ = ctx
	needle := strings.ToLower(strings.TrimSpace(q))
	if needle == "" {
		return nil, errors.New("query required")
	}
	ids := make([]string, 0, len(s.Authorized))
	for id := range s.Authorized {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []CatalogItem
	for _, id := range ids {
		msgs, err := runtime.LoadConversation(s.Authorized[id])
		if err != nil {
			continue
		}
		digest := conversationDigest(msgs)
		for n, m := range msgs {
			text := messageText(m)
			if strings.Contains(strings.ToLower(text), needle) {
				out = append(out, CatalogItem{Ref: Ref{Kind: "session", ID: id, Locator: "message:" + strconv.Itoa(n), Digest: digest}, Summary: boundedExcerpt(text, needle, 240)})
				if len(out) >= max {
					return out, nil
				}
			}
		}
	}
	return out, nil
}
func (s SessionCorpus) Open(ctx context.Context, ref Ref) (Opened, error) {
	_ = ctx
	path, ok := s.Authorized[ref.ID]
	if !ok || ref.Kind != "session" {
		return Opened{}, errors.New("session not found")
	}
	msgs, err := runtime.LoadConversation(path)
	if err != nil {
		return Opened{}, errors.New("session not found")
	}
	digest := conversationDigest(msgs)
	if ref.Digest != "" && ref.Digest != digest {
		return Opened{}, errors.New("session digest mismatch")
	}
	start, end := 0, len(msgs)
	if strings.HasPrefix(ref.Locator, "message:") {
		n, err := strconv.Atoi(strings.TrimPrefix(ref.Locator, "message:"))
		if err != nil || n < 0 || n >= len(msgs) {
			return Opened{}, errors.New("invalid message locator")
		}
		start = n - 2
		if start < 0 {
			start = 0
		}
		end = n + 3
		if end > len(msgs) {
			end = len(msgs)
		}
	}
	capMessages := s.MaxMessages
	if capMessages <= 0 || capMessages > 100 {
		capMessages = 20
	}
	if end-start > capMessages {
		end = start + capMessages
	}
	view := make([]struct {
		Index int        `json:"index"`
		Role  agent.Role `json:"role"`
		Text  string     `json:"text"`
	}, 0, end-start)
	for n := start; n < end; n++ {
		view = append(view, struct {
			Index int        `json:"index"`
			Role  agent.Role `json:"role"`
			Text  string     `json:"text"`
		}{n, msgs[n].Role, messageText(msgs[n])})
	}
	body, _ := json.Marshal(view)
	actual := Ref{Kind: "session", ID: ref.ID, Locator: fmt.Sprintf("messages:%d-%d", start, end), Digest: digest}
	return Opened{Ref: actual, Body: string(body)}, nil
}
func conversationDigest(msgs []agent.Message) string { b, _ := json.Marshal(msgs); return Digest(b) }
func messageText(m agent.Message) string {
	var parts []string
	for _, b := range m.Content {
		switch {
		case b.Text != nil:
			parts = append(parts, b.Text.Text)
		case b.ToolUse != nil:
			parts = append(parts, b.ToolUse.Name+" "+string(b.ToolUse.Input))
		case b.ToolResult != nil:
			parts = append(parts, b.ToolResult.Content)
		}
	}
	return strings.Join(parts, "\n")
}
func boundedExcerpt(text, needle string, max int) string {
	text = strings.TrimSpace(text)
	lower := strings.ToLower(text)
	at := strings.Index(lower, needle)
	if at < 0 || len(text) <= max {
		return text
	}
	start := at - max/2
	if start < 0 {
		start = 0
	}
	end := start + max
	if end > len(text) {
		end = len(text)
	}
	return text[start:end]
}
