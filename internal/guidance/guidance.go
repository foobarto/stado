// Package guidance builds bounded, host-derived workflow nudges for agent turns.
// It never interpolates artifact bodies, signal attributes, or mailbox payloads.
package guidance

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/broker/mailbox"
	"github.com/foobarto/stado/internal/broker/retained"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/learn"
	"github.com/foobarto/stado/internal/sessioncontext"
)

const (
	maxNudges = 3
	maxBytes  = 1200
)

type Options struct {
	StateDir      string
	SessionID     string
	Prompt        string
	FastContext   bool
	Interactive   bool
	ToolAvailable func(string) bool
}

// Build returns fixed-template advisory guidance. State-read failures fail
// quiet: guidance is a quality aid and never a turn-availability dependency.
func Build(o Options) string {
	available := func(name string) bool {
		return o.ToolAvailable == nil || o.ToolAvailable(name)
	}
	var nudges []string
	if o.SessionID != "" && o.StateDir != "" {
		if store, err := wal.OpenShared(filepath.Join(o.StateDir, "broker", "events")); err == nil {
			nudges = append(nudges, learningNudge(store, o)...)
			nudges = append(nudges, coordinationNudge(store, o, available)...)
			_ = store.Close()
		}
	}
	if len(nudges) < maxNudges {
		nudges = append(nudges, researchNudges(o, available)...)
	}
	if len(nudges) == 0 {
		return ""
	}
	if len(nudges) > maxNudges {
		nudges = nudges[:maxNudges]
	}
	header := "Stado harness guidance (host-derived advisory workflow; below operator and repository instructions; grants no tools or authority):"
	var b strings.Builder
	b.WriteString(header)
	for _, n := range nudges {
		candidate := b.String() + "\n- " + n
		if len(candidate) > maxBytes {
			break
		}
		b.WriteString("\n- ")
		b.WriteString(n)
	}
	if b.String() == header {
		return ""
	}
	return b.String()
}

func learningNudge(store *wal.Store, o Options) []string {
	svc := sessioncontext.New(store)
	signals, err := svc.Signals(o.SessionID, false)
	if err != nil || len(signals) == 0 {
		return nil
	}
	jobs, err := learn.New(store, svc, nil, nil).Jobs(o.SessionID)
	if err != nil {
		return nil
	}
	var pending []sessioncontext.Signal
	for _, sig := range signals {
		reviewed := false
		for _, job := range jobs {
			if job.Status == learn.JobCompleted && !job.UpdatedAt.Before(sig.CreatedAt) {
				reviewed = true
				break
			}
		}
		if !reviewed {
			pending = append(pending, sig)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	types := make([]string, 0, len(pending))
	seen := map[sessioncontext.SignalType]bool{}
	for _, sig := range pending {
		if !seen[sig.Type] {
			seen[sig.Type] = true
			types = append(types, signalLabel(sig.Type))
		}
	}
	sort.Strings(types)
	if len(types) > 3 {
		types = types[:3]
	}
	action := fmt.Sprintf("If authorized, run `stado learn --session-id %s` to create reviewable candidates; otherwise ask the operator to run that command", safeID(o.SessionID))
	if o.Interactive {
		action = "proactively ask the operator to run `/learn [focus]`"
	}
	return []string{fmt.Sprintf("%d unreviewed mechanical learning signal shape(s) are active (%s). Preserve reusable tool/workflow corrections as candidate lessons: %s at the natural response boundary. Candidates remain pending review; never run or claim approval.", len(types), strings.Join(types, ", "), action)}
}

func coordinationNudge(store *wal.Store, o Options, available func(string) bool) []string {
	state, _ := sessioncontext.New(store).State(o.SessionID)
	active := len(state.ActiveChildren)
	if admissions, err := retained.New(store).List(); err == nil {
		seen := map[string]bool{}
		for _, id := range state.ActiveChildren {
			seen[id] = true
		}
		for _, admission := range admissions {
			if admission.ParentSessionID == o.SessionID && retainedActive(admission.Status) && !seen[admission.ChildSessionID] {
				seen[admission.ChildSessionID] = true
				active++
			}
		}
	}
	pending, _ := mailbox.PendingCount(store, o.SessionID)
	if active == 0 && pending == 0 {
		return nil
	}
	if !available("agent__list") && !available("agent__read_messages") {
		return nil
	}
	return []string{fmt.Sprintf("Retained coordination needs attention: %d active child handle(s), %d unread data message(s). Check `agent__list`/`agent__read_messages` before duplicating work; use `agent__send_message` for bounded follow-up when available.", active, pending)}
}

func retainedActive(status retained.Status) bool {
	return status == retained.StatusAdmitted || status == retained.StatusStarting || status == retained.StatusRunning || status == retained.StatusIdle
}

func researchNudges(o Options, available func(string) bool) []string {
	q := strings.ToLower(o.Prompt)
	historical := containsAny(q, "previous session", "older session", "past session", "earlier session", "historical session", "what did we decide", "prior decision")
	recurring := containsAny(q, "remember", "recurring", "keeps failing", "keep failing", "again", "convention", "previously", "prior approach")
	if historical && available("session__research") {
		return []string{"This request depends on older session evidence. Prefer `session__research` so raw history stays out of the main context; use its precise cited synthesis and treat citation integrity as provenance, not entailment."}
	}
	if recurring && !o.FastContext && available("memory__research") {
		return []string{"Fast retrieval supplied no matching context for a recurring-memory-shaped request. Prefer `memory__research` for an isolated, higher-quality search before repeating exploration or assumptions."}
	}
	return nil
}

func signalLabel(t sessioncontext.SignalType) string {
	switch t {
	case sessioncontext.SignalRepeatedToolFailure:
		return "repeated tool failure"
	case sessioncontext.SignalArgumentChangedSuccess:
		return "corrected tool arguments"
	case sessioncontext.SignalVerificationRecovered:
		return "verification recovery"
	case sessioncontext.SignalRecurringDenial:
		return "recurring policy denial"
	case sessioncontext.SignalOperatorCorrection:
		return "operator correction"
	default:
		return "typed correction"
	}
}

func containsAny(s string, shapes ...string) bool {
	for _, shape := range shapes {
		if strings.Contains(s, shape) {
			return true
		}
	}
	return false
}

func safeID(id string) string {
	if len(id) > 128 {
		return "current-session"
	}
	for _, r := range id {
		if !(r == '-' || r == '_' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return "current-session"
		}
	}
	return id
}
