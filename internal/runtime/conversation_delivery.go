package runtime

// Idempotent receiver evidence for broker-owned operator input. The broker WAL
// remains the input/task authority. This structured conversation record proves
// only that one exact session appended the immutable text identified by a
// broker-minted delivery ID before the native controller committed delivery.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"

	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
)

const conversationDeliverySchemaV1 = "stado.dev/conversation-message-delivery/v1"

type conversationMessageDelivery struct {
	Type        string          `json:"type"`
	Schema      string          `json:"schema"`
	SessionID   string          `json:"session_id"`
	DeliveryID  string          `json:"delivery_id"`
	TextDigests []string        `json:"text_digests"`
	Messages    []agent.Message `json:"messages"`
}

var conversationDeliveryMu sync.Mutex

// AppendConversationMessageDelivery appends an exact batch once. Every
// message must be a single plain user-text message and its supplied digest must
// match those exact UTF-8 bytes. A replay of the same broker delivery ID and
// identical record succeeds; any mismatch is corruption and fails closed.
func AppendConversationMessageDelivery(session *stadogit.Session, deliveryID string, digests []string, messages []agent.Message) (bool, error) {
	if session == nil || strings.TrimSpace(session.ID) == "" || strings.TrimSpace(session.WorktreePath) == "" {
		return false, errors.New("conversation delivery: exact session required")
	}
	if invalidConversationDeliveryID(deliveryID) || len(messages) == 0 || len(messages) != len(digests) {
		return false, errors.New("conversation delivery: invalid delivery record")
	}
	for i, message := range messages {
		text, ok := plainUserText(message)
		if !ok || receiverTextDigest(text) != digests[i] {
			return false, fmt.Errorf("conversation delivery: immutable text digest mismatch at message %d", i)
		}
	}
	record := conversationMessageDelivery{
		Type: "message", Schema: conversationDeliverySchemaV1,
		SessionID: session.ID, DeliveryID: deliveryID,
		TextDigests: append([]string(nil), digests...), Messages: append([]agent.Message(nil), messages...),
	}

	conversationDeliveryMu.Lock()
	defer conversationDeliveryMu.Unlock()
	raw, err := RawConversationLog(session.WorktreePath)
	if err != nil {
		return false, err
	}
	existing, found, err := findConversationDelivery(raw, deliveryID)
	if err != nil {
		return false, err
	}
	if found {
		if !reflect.DeepEqual(existing, record) {
			return false, errors.New("conversation delivery: delivery ID record mismatch")
		}
		return false, nil
	}
	if err := appendConversationRecord(session.WorktreePath, record); err != nil {
		return false, err
	}
	return true, nil
}

func findConversationDelivery(raw []byte, deliveryID string) (conversationMessageDelivery, bool, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), int(maxConversationRecordBytes))
	var existing conversationMessageDelivery
	found := false
	for scanner.Scan() {
		line := scanner.Bytes()
		var probe struct {
			DeliveryID string `json:"delivery_id"`
		}
		probeDecoder := json.NewDecoder(bytes.NewReader(line))
		if probeDecoder.Decode(&probe) != nil || probe.DeliveryID != deliveryID {
			continue
		}
		record, err := strictDecodeConversationDelivery(line)
		if err != nil {
			return conversationMessageDelivery{}, false, err
		}
		if found {
			return conversationMessageDelivery{}, false, errors.New("conversation delivery: duplicate delivery ID evidence")
		}
		existing, found = record, true
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return conversationMessageDelivery{}, false, err
	}
	return existing, found, nil
}

func strictDecodeConversationDelivery(line []byte) (conversationMessageDelivery, error) {
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.DisallowUnknownFields()
	var record conversationMessageDelivery
	if err := decoder.Decode(&record); err != nil {
		return conversationMessageDelivery{}, fmt.Errorf("conversation delivery: malformed existing delivery record: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("trailing JSON value")
		}
		return conversationMessageDelivery{}, fmt.Errorf("conversation delivery: malformed existing delivery record: %w", err)
	}
	if record.Type != "message" || record.Schema != conversationDeliverySchemaV1 || invalidConversationDeliveryID(record.SessionID) || invalidConversationDeliveryID(record.DeliveryID) || len(record.Messages) == 0 || len(record.Messages) != len(record.TextDigests) {
		return conversationMessageDelivery{}, errors.New("conversation delivery: malformed existing delivery record: invalid semantic shape")
	}
	for i, message := range record.Messages {
		text, ok := plainUserText(message)
		if !ok || receiverTextDigest(text) != record.TextDigests[i] {
			return conversationMessageDelivery{}, fmt.Errorf("conversation delivery: malformed existing delivery record: immutable text digest mismatch at message %d", i)
		}
	}
	return record, nil
}

func plainUserText(message agent.Message) (string, bool) {
	if message.Role != agent.RoleUser || len(message.Content) != 1 || message.Content[0].Text == nil {
		return "", false
	}
	return message.Content[0].Text.Text, true
}

func receiverTextDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func invalidConversationDeliveryID(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value {
		return true
	}
	for _, r := range value {
		if r < 0x21 || r == 0x7f {
			return true
		}
	}
	return false
}
