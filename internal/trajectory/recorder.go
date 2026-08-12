package trajectory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/sessioncontext"
	"github.com/foobarto/stado/pkg/agent"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

type Recorder struct{ StateDir, SessionID, Principal string }

func LocalPrincipal() string {
	if u, err := user.Current(); err == nil && u.Uid != "" {
		return "os-user:" + u.Uid
	}
	return "local"
}
func (r Recorder) EnsureObjective(objective string) {
	if r.SessionID == "" || r.StateDir == "" || strings.TrimSpace(objective) == "" {
		return
	}
	store, err := wal.OpenShared(filepath.Join(r.StateDir, "broker", "events"))
	if err != nil {
		return
	}
	defer func() { _ = store.Close() }()
	svc := sessioncontext.New(store)
	state, err := svc.State(r.SessionID)
	if err != nil || state.Objective != "" {
		return
	}
	objective = strings.TrimSpace(objective)
	_, _ = svc.PatchHost(context.Background(), r.SessionID, r.Principal, "runtime", "objective:"+r.SessionID, state.Version, sessioncontext.HostPatch{Objective: &objective})
}
func (r Recorder) ToolOutcome(turn int, call agent.ToolUseBlock, result agent.ToolResultBlock) {
	if r.SessionID == "" || r.StateDir == "" {
		return
	}
	store, err := wal.OpenShared(filepath.Join(r.StateDir, "broker", "events"))
	if err != nil {
		return
	}
	defer func() { _ = store.Close() }()
	sum := sha256.Sum256(call.Input)
	obs := sessioncontext.Observation{SessionID: r.SessionID, Kind: sessioncontext.ObservationTool, Tool: call.Name, ArgsDigest: hex.EncodeToString(sum[:]), Succeeded: !result.IsError, EvidenceRef: fmt.Sprintf("session:%s/turn:%d/tool:%s", r.SessionID, turn, call.ID), Attributes: map[string]string{}}
	if result.IsError && (strings.Contains(strings.ToLower(result.Content), "permission") || strings.Contains(strings.ToLower(result.Content), "outside write_scope") || strings.Contains(strings.ToLower(result.Content), "denied")) {
		obs.Kind = sessioncontext.ObservationDenial
		obs.Attributes["boundary"] = call.Name
	}
	svc := sessioncontext.New(store)
	_, _ = svc.Observe(context.Background(), obs, r.Principal, "runtime", fmt.Sprintf("trajectory:%s:%d:%s:%d", r.SessionID, turn, call.ID, time.Now().UnixNano()))
}
