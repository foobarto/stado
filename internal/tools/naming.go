package tools

import (
	"fmt"
	"strings"
)

// WireForm synthesises the LLM-facing tool name from a plugin's local alias
// and tool name. Dots and dashes in either segment become underscores; the
// double-underscore separator is reserved and rejected in inputs.
//
// Examples:
//
//	WireForm("fs", "read")     → "fs__read"
//	WireForm("htb-lab", "spawn") → "htb_lab__spawn"
//	WireForm("tools", "search") → "tools__search"
func WireForm(localAlias, toolName string) (string, error) {
	if strings.Contains(localAlias, "__") {
		return "", fmt.Errorf("naming: local alias %q contains reserved separator __", localAlias)
	}
	if strings.Contains(toolName, "__") {
		return "", fmt.Errorf("naming: tool name %q contains reserved separator __", toolName)
	}
	seg := func(s string) string {
		s = strings.ReplaceAll(s, ".", "_")
		s = strings.ReplaceAll(s, "-", "_")
		return s
	}
	wire := seg(localAlias) + "__" + seg(toolName)
	if len(wire) > 64 {
		return "", fmt.Errorf("naming: wire form %q exceeds 64 chars (Anthropic tool name limit)", wire)
	}
	return wire, nil
}

// ParseWireForm splits a wire-form tool name back into (localAlias, toolName).
// Returns ok=false if the string contains no __ separator.
func ParseWireForm(wire string) (localAlias, toolName string, ok bool) {
	idx := strings.Index(wire, "__")
	if idx < 0 {
		return "", "", false
	}
	return wire[:idx], wire[idx+2:], true
}
