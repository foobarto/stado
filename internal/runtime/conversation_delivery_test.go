package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
)

func TestConversationMessageDeliveryCrashReplayIsIdempotentAndMismatchFailsClosed(t *testing.T) {
	session := &stadogit.Session{ID: "session-delivery", WorktreePath: t.TempDir()}
	text := "preserve this exact operator request"
	digest := receiverTextDigest(text)
	message := agent.Text(agent.RoleUser, text)

	// Crash before append leaves no receiver evidence; retry appends it.
	if loaded, err := LoadConversation(session.WorktreePath); err != nil || len(loaded) != 0 {
		t.Fatalf("pre-append conversation = %#v, %v", loaded, err)
	}
	if _, err := AppendConversationMessageDelivery(session, "operator_input_broker_minted", []string{digest}, []agent.Message{message}); err != nil {
		t.Fatal(err)
	}
	// Crash after append but before broker commit replays the same delivery.
	if appended, err := AppendConversationMessageDelivery(session, "operator_input_broker_minted", []string{digest}, []agent.Message{message}); err != nil || appended {
		t.Fatal(err)
	}
	loaded, err := LoadConversation(session.WorktreePath)
	if err != nil || len(loaded) != 1 || deliveryLatestUserPrompt(loaded) != text {
		t.Fatalf("replayed delivery = %#v, %v", loaded, err)
	}

	changed := agent.Text(agent.RoleUser, "different text")
	if _, err := AppendConversationMessageDelivery(session, "operator_input_broker_minted", []string{receiverTextDigest("different text")}, []agent.Message{changed}); err == nil || !strings.Contains(err.Error(), "record mismatch") {
		t.Fatalf("same-ID changed-message error = %v", err)
	}
	if _, err := AppendConversationMessageDelivery(session, "other_broker_id", []string{digest}, []agent.Message{changed}); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("wrong broker digest error = %v", err)
	}
	loaded, _ = LoadConversation(session.WorktreePath)
	if len(loaded) != 1 || deliveryLatestUserPrompt(loaded) != text {
		t.Fatalf("mismatch changed conversation = %#v", loaded)
	}
}

func TestConversationMessageDeliveryBatchSurvivesCompactionAndReopen(t *testing.T) {
	session := &stadogit.Session{ID: "session-continuation", WorktreePath: t.TempDir()}
	if err := AppendMessage(session.WorktreePath, agent.Text(agent.RoleUser, "old prompt")); err != nil {
		t.Fatal(err)
	}
	if err := AppendCompaction(session.WorktreePath, ConversationCompaction{Summary: "compacted history", FromTurn: 1, ToTurn: 1, TurnsTotal: 1}); err != nil {
		t.Fatal(err)
	}
	texts := []string{"first exact deferred request", "second exact deferred request"}
	messages := []agent.Message{agent.Text(agent.RoleUser, texts[0]), agent.Text(agent.RoleUser, texts[1])}
	digests := []string{receiverTextDigest(texts[0]), receiverTextDigest(texts[1])}
	if _, err := AppendConversationMessageDelivery(session, "continuation_delivery_broker_minted", digests, messages); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadConversation(session.WorktreePath)
	if err != nil || len(loaded) != 3 {
		t.Fatalf("reopened compacted delivery = %#v, %v", loaded, err)
	}
	if deliveryLatestUserPrompt(loaded[:2]) != texts[0] || deliveryLatestUserPrompt(loaded) != texts[1] {
		t.Fatalf("ordered continuation messages = %#v", loaded)
	}
	// Crash after the whole batch append but before continuation commit is an
	// exact replay, never a second pair of user messages.
	if appended, err := AppendConversationMessageDelivery(session, "continuation_delivery_broker_minted", digests, messages); err != nil || appended {
		t.Fatal(err)
	}
	loaded, _ = LoadConversation(session.WorktreePath)
	if len(loaded) != 3 {
		t.Fatalf("batch replay duplicated messages: %#v", loaded)
	}
}

func TestConversationMessageDeliveryFramesOffPartialTailAndRemainsVisible(t *testing.T) {
	session := &stadogit.Session{ID: "session-partial-tail", WorktreePath: t.TempDir()}
	if err := AppendMessage(session.WorktreePath, agent.Text(agent.RoleUser, "before crash")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(session.WorktreePath, ConversationFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"type":"message","schema":"stado.dev/conversation-message-delivery/v1","delivery_id":"crash`); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	text := "after partial tail"
	message := agent.Text(agent.RoleUser, text)
	digest := receiverTextDigest(text)
	if appended, err := AppendConversationMessageDelivery(session, "operator_input_after_partial", []string{digest}, []agent.Message{message}); err != nil || !appended {
		t.Fatalf("post-partial append=%v err=%v", appended, err)
	}
	loaded, err := LoadConversation(session.WorktreePath)
	if err != nil || len(loaded) != 2 || deliveryLatestUserPrompt(loaded) != text {
		t.Fatalf("post-partial reopen=%#v err=%v", loaded, err)
	}
	if appended, err := AppendConversationMessageDelivery(session, "operator_input_after_partial", []string{digest}, []agent.Message{message}); err != nil || appended {
		t.Fatalf("post-partial replay=%v err=%v", appended, err)
	}
	raw, err := RawConversationLog(session.WorktreePath)
	if err != nil || !strings.Contains(string(raw), "crash\n{\"type\":\"message\"") {
		t.Fatalf("partial framing raw=%q err=%v", raw, err)
	}
}

func TestConversationMessageDeliveryExistingEvidenceRejectsUnknownAndTrailingJSON(t *testing.T) {
	text := "strict evidence"
	message := agent.Text(agent.RoleUser, text)
	digest := receiverTextDigest(text)
	base := conversationMessageDelivery{
		Type: "message", Schema: conversationDeliverySchemaV1, SessionID: "strict-session",
		DeliveryID: "operator_input_strict", TextDigests: []string{digest}, Messages: []agent.Message{message},
	}
	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	unknown := append([]byte(nil), encoded[:len(encoded)-1]...)
	unknown = append(unknown, []byte(",\"unknown\":\"must fail\"}\n")...)

	tests := []struct {
		name string
		line []byte
	}{
		{name: "unknown field", line: unknown},
		{name: "trailing JSON", line: append(append([]byte(nil), encoded...), []byte(" {}\n")...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			session := &stadogit.Session{ID: base.SessionID, WorktreePath: t.TempDir()}
			if err := os.MkdirAll(filepath.Join(session.WorktreePath, ".stado"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(session.WorktreePath, ConversationFile), test.line, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := AppendConversationMessageDelivery(session, base.DeliveryID, []string{digest}, []agent.Message{message}); err == nil || !strings.Contains(err.Error(), "malformed existing delivery record") {
				t.Fatalf("strict existing evidence error=%v", err)
			}
		})
	}
}

func TestLoadConversationRejectsSemanticallyForgedDeliveryEvidence(t *testing.T) {
	validText := "valid user text"
	tests := []struct {
		name   string
		record conversationMessageDelivery
	}{
		{
			name: "bad digest",
			record: conversationMessageDelivery{
				Type: "message", Schema: conversationDeliverySchemaV1, SessionID: "semantic-session",
				DeliveryID: "operator_input_bad_digest", TextDigests: []string{receiverTextDigest("different")},
				Messages: []agent.Message{agent.Text(agent.RoleUser, validText)},
			},
		},
		{
			name: "non-user message",
			record: conversationMessageDelivery{
				Type: "message", Schema: conversationDeliverySchemaV1, SessionID: "semantic-session",
				DeliveryID: "operator_input_bad_role", TextDigests: []string{receiverTextDigest(validText)},
				Messages: []agent.Message{agent.Text(agent.RoleAssistant, validText)},
			},
		},
		{
			name: "empty batch",
			record: conversationMessageDelivery{
				Type: "message", Schema: conversationDeliverySchemaV1, SessionID: "semantic-session",
				DeliveryID: "operator_input_empty",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			worktree := t.TempDir()
			if err := appendConversationRecord(worktree, test.record); err != nil {
				t.Fatal(err)
			}
			if loaded, err := LoadConversation(worktree); err == nil || !strings.Contains(err.Error(), "malformed existing delivery record") || len(loaded) != 0 {
				t.Fatalf("forged evidence loaded=%#v err=%v", loaded, err)
			}
		})
	}
}

func TestConversationMessageDeliveryRejectsLaterConflictingDuplicateID(t *testing.T) {
	session := &stadogit.Session{ID: "duplicate-session", WorktreePath: t.TempDir()}
	firstText := "first exact text"
	firstMessage := agent.Text(agent.RoleUser, firstText)
	firstDigest := receiverTextDigest(firstText)
	if _, err := AppendConversationMessageDelivery(session, "operator_input_duplicate", []string{firstDigest}, []agent.Message{firstMessage}); err != nil {
		t.Fatal(err)
	}
	secondText := "conflicting later text"
	second := conversationMessageDelivery{
		Type: "message", Schema: conversationDeliverySchemaV1, SessionID: session.ID,
		DeliveryID: "operator_input_duplicate", TextDigests: []string{receiverTextDigest(secondText)},
		Messages: []agent.Message{agent.Text(agent.RoleUser, secondText)},
	}
	if err := appendConversationRecord(session.WorktreePath, second); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendConversationMessageDelivery(session, "operator_input_duplicate", []string{firstDigest}, []agent.Message{firstMessage}); err == nil || !strings.Contains(err.Error(), "duplicate delivery ID") {
		t.Fatalf("duplicate replay error=%v", err)
	}
	if loaded, err := LoadConversation(session.WorktreePath); err == nil || !strings.Contains(err.Error(), "duplicate delivery ID") || len(loaded) != 1 || deliveryLatestUserPrompt(loaded) != firstText {
		t.Fatalf("duplicate load=%#v err=%v", loaded, err)
	}
}

func deliveryLatestUserPrompt(messages []agent.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if text, ok := plainUserText(messages[i]); ok {
			return text
		}
	}
	return ""
}
