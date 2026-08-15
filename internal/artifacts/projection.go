package artifacts

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Projection is the bounded, generic search/display view declared by a
// plugin's signed and WAL-archived kind descriptor. It contains no built-in
// artifact-kind knowledge.
type Projection struct {
	Title   string `json:"title,omitempty"`
	Text    string `json:"text,omitempty"`
	Trigger string `json:"trigger,omitempty"`
}

func resolveJSONPointer(raw json.RawMessage, pointer string) (any, bool) {
	var value any
	if json.Unmarshal(raw, &value) != nil || pointer == "" || pointer[0] != '/' {
		return nil, false
	}
	current := value
	for _, token := range splitJSONPointer(pointer) {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[token]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func splitJSONPointer(pointer string) []string {
	var out []string
	start := 1
	for i := 1; i <= len(pointer); i++ {
		if i != len(pointer) && pointer[i] != '/' {
			continue
		}
		token := pointer[start:i]
		token = strings.ReplaceAll(strings.ReplaceAll(token, "~1", "/"), "~0", "~")
		out = append(out, token)
		start = i + 1
	}
	return out
}

// Project validates the artifact against its exact archived descriptor before
// resolving signed title/text/trigger JSON-pointer roles.
func (s *Service) Project(item Artifact) (Projection, error) {
	if s == nil || s.wal == nil {
		return Projection{}, errors.New("artifact projection unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	descriptor, ok := descriptorForArtifact(archivedKindDescriptors(s.wal.Records()), item)
	if !ok {
		return Projection{}, fmt.Errorf("artifact %q has no exact archived kind descriptor", item.ID)
	}
	title, text, trigger, err := projectIndexText(item.Data, descriptor)
	if err != nil {
		return Projection{}, err
	}
	return Projection{Title: title, Text: text, Trigger: trigger}, nil
}
