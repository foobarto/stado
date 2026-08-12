package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	brokerbudget "github.com/foobarto/stado/internal/broker/budget"
	"github.com/foobarto/stado/internal/broker/mailbox"
	"github.com/foobarto/stado/internal/broker/retained"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/orchestration"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/subagent"
)

type retainedRuntimeState struct {
	store                *wal.Store
	coordinator          *orchestration.Coordinator
	accountID, principal string
}

var retainedRuntimes = struct {
	sync.Mutex
	bySession map[string]*retainedRuntimeState
}{bySession: map[string]*retainedRuntimeState{}}

// ConfigureRetainedBridge attaches the durable child registry, recursive
// budget ledger, and mailbox to an existing public fleet bridge.
func ConfigureRetainedBridge(ctx context.Context, cfg *config.Config, parent *stadogit.Session, bridge *FleetBridgeAdapter) (func() error, error) {
	if cfg == nil || parent == nil || bridge == nil || bridge.Spawner == nil {
		return nil, fmt.Errorf("retained bridge requires config, parent, bridge, and spawner")
	}
	key := filepath.Clean(cfg.StateDir()) + "\x00" + parent.ID
	retainedRuntimes.Lock()
	defer retainedRuntimes.Unlock()
	principal := "local"
	if current, lookupErr := user.Current(); lookupErr == nil && current.Uid != "" {
		principal = "os-user:" + current.Uid
	}
	state := retainedRuntimes.bySession[key]
	if state == nil {
		store, err := wal.OpenShared(filepath.Join(cfg.StateDir(), "broker", "events"))
		if err != nil {
			return nil, err
		}
		ledger := brokerbudget.New(store)
		accountID := "session:" + parent.ID
		if _, ok, getErr := ledger.GetAccount(accountID); getErr != nil {
			_ = store.Close()
			return nil, getErr
		} else if !ok {
			_, err = ledger.CreateAccount(ctx, accountID, "", brokerbudget.Limits{Tokens: 2_000_000, ToolCalls: 10_000, Turns: 2_000, WallSeconds: 86_400}, principal, "broker", "retained-account:"+parent.ID)
			if err != nil {
				_ = store.Close()
				return nil, err
			}
		}
		policy := mailbox.NewDynamicRelationPolicy()
		mail := mailbox.New(store, policy)
		coord := orchestration.New(retained.New(store), ledger, mail, nil)
		coord.Policy = policy
		state = &retainedRuntimeState{store: store, coordinator: coord, accountID: accountID, principal: principal}
		retainedRuntimes.bySession[key] = state
	}
	bridge.Retained, bridge.RetainedAccountID = state.coordinator, state.accountID
	bridge.Principal, bridge.ParentSessionID = state.principal, parent.ID
	bridge.ResolveForkPoint = func(callCtx context.Context, req pluginRuntime.AgentSpawnRequest) (retained.ForkPoint, error) {
		source := parent
		var err error
		selector := "last_committed_turn"
		if req.Source != nil {
			selector = req.Source.At
			if selector == "" {
				selector = "last_committed_turn"
			}
			resolver := ResolveTreeSource(parent, cfg.WorktreeDir())
			source, err = resolver(callCtx, subagent.Source{SessionID: req.Source.SessionID, At: selector})
			if err != nil {
				return retained.ForkPoint{}, err
			}
		}
		tree, err := source.TreeHead()
		if err != nil {
			return retained.ForkPoint{}, err
		}
		turn := source.Turn()
		if strings.HasPrefix(selector, "turns/") {
			turn, err = strconv.Atoi(strings.TrimPrefix(selector, "turns/"))
			if err != nil {
				return retained.ForkPoint{}, err
			}
			tree, err = source.Sidecar.ResolveRef(stadogit.TurnTagRef(source.ID, turn))
			if err != nil {
				return retained.ForkPoint{}, err
			}
		}
		trace, err := source.TraceHead()
		if err != nil {
			return retained.ForkPoint{}, err
		}
		seed, err := historicalSeed(source, selector)
		if err != nil {
			return retained.ForkPoint{}, err
		}
		seedBytes, _ := json.Marshal(seed)
		digest := sha256.Sum256(seedBytes)
		treeText, traceText := tree.String(), trace.String()
		if tree.IsZero() {
			treeText = "empty"
		}
		if trace.IsZero() {
			traceText = "empty"
		}
		return retained.ForkPoint{SourceSessionID: source.ID, SourceGeneration: 1, CommittedTurn: turn, ConversationDigest: hex.EncodeToString(digest[:]), TreeCommit: treeText, TraceCommit: traceText, ResolvedAt: time.Now().UTC()}, nil
	}
	// The process-local broker owns this handle until process exit; callers may
	// create a fresh bridge each turn without cancelling retained children.
	return func() error { return nil }, nil
}
