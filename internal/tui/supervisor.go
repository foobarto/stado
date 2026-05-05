package tui

// supervisor.go implements the EP-0033 responsive frontline: a supervisor
// lane that handles user input during worker turns without blocking.
//
// The supervisor is the evolved /btw: instead of a one-shot side query,
// inputs classified as supervisor-bound stream a reply immediately while
// the worker continues. Inputs classified as queue/steer get deferred.

import (
	"strings"
)

// supervisorClass is the output of the classifier head.
type supervisorClass int

const (
	// supervisorAnswer: supervisor handles this input directly, streams reply.
	supervisorAnswer supervisorClass = iota
	// supervisorQueue: append to worker queue; handled at next worker turn.
	supervisorQueue
	// supervisorSteer: inject a steering note into the shared transcript.
	supervisorSteer
	// supervisorInterrupt: cancel worker at next tool boundary, then queue.
	supervisorInterrupt
)

// classifyInput runs a rule-based classifier on user input.
// EP-0033 §Classifier head. A prompt-based classifier is possible
// but the rule-based version handles the common cases cheaply:
//   - Ends with "?" → answer (supervisor replies directly)
//   - Starts with steer prefix → steer
//   - Otherwise → queue
//
// The rule-based classifier can be replaced with a prompt-based one
// by setting [supervisor.classifier_prompt] in config.
func classifyInput(text string) supervisorClass {
	t := strings.TrimSpace(text)
	if t == "" {
		return supervisorQueue
	}
	// Interrupt keywords — user explicitly wants to stop the worker.
	lower := strings.ToLower(t)
	for _, kw := range interruptPhrases {
		if strings.HasPrefix(lower, kw) {
			return supervisorInterrupt
		}
	}
	// Steer keywords — user wants to guide the worker without stopping it.
	for _, kw := range steerPhrases {
		if strings.HasPrefix(lower, kw) {
			return supervisorSteer
		}
	}
	// Question heuristic — ends with ? after stripping trailing punctuation.
	stripped := strings.TrimRight(t, " \t")
	if strings.HasSuffix(stripped, "?") {
		return supervisorAnswer
	}
	// Short inputs (<= 8 words) that don't start with action verbs are
	// more likely observational questions than commands.
	words := strings.Fields(t)
	if len(words) <= 8 && !startsWithActionVerb(words[0]) {
		return supervisorAnswer
	}
	return supervisorQueue
}

var interruptPhrases = []string{
	"stop", "cancel", "abort", "halt", "wait", "don't",
	"do not", "never mind", "ignore that",
}

var steerPhrases = []string{
	"actually", "instead", "but ", "also ", "and also",
	"after that", "then ", "before that",
}

func startsWithActionVerb(word string) bool {
	w := strings.ToLower(word)
	for _, v := range actionVerbs {
		if w == v {
			return true
		}
	}
	return false
}

var actionVerbs = []string{
	"add", "create", "write", "update", "edit", "fix", "change", "delete",
	"remove", "rename", "move", "run", "execute", "build", "compile", "test",
	"deploy", "push", "pull", "commit", "install", "generate", "implement",
	"refactor", "migrate", "check", "verify", "make", "set",
}
