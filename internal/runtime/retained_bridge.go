package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/broker/retained"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/orchestration"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/subagent"
)

// RetainedBackendBinding is native controller authority over one exact
// logical session's broker-owned retained-child state. The opaque backend
// holds the bearer; only non-secret scope metadata is projected here.
type RetainedBackendBinding struct {
	Backend         orchestration.RetainedBackend
	AccountID       string
	Principal       string
	ParentSessionID string
}

// RetainedBackendProvider is implemented by daemon-backed native controllers.
// There is intentionally no filesystem fallback: canonical admission,
// budgets, lifecycle, and mailboxes stay behind authenticated broker RPC.
type RetainedBackendProvider interface {
	BindRetainedBackend(context.Context) (RetainedBackendBinding, error)
}

// ConfigureRetainedBridge attaches the broker-owned durable child registry,
// recursive budget ledger, and mailbox to an existing public fleet bridge.
func ConfigureRetainedBridge(ctx context.Context, cfg *config.Config, parent *stadogit.Session, bridge *FleetBridgeAdapter, controller BrokerController) (func() error, error) {
	if cfg == nil || parent == nil || bridge == nil || bridge.Spawner == nil || controller == nil {
		return nil, fmt.Errorf("retained bridge requires config, parent, bridge, spawner, and broker controller")
	}
	provider, ok := controller.(RetainedBackendProvider)
	if !ok {
		return nil, fmt.Errorf("retained execution requires an authenticated broker backend")
	}
	binding, err := provider.BindRetainedBackend(ctx)
	if err != nil {
		return nil, err
	}
	if binding.Backend == nil || binding.AccountID == "" || binding.Principal == "" || binding.ParentSessionID != parent.ID {
		return nil, fmt.Errorf("retained broker binding does not match the active logical session")
	}
	bridge.Retained = orchestration.NewBrokerCoordinator(binding.Backend)
	bridge.RetainedAccountID = binding.AccountID
	bridge.Principal, bridge.ParentSessionID = binding.Principal, binding.ParentSessionID
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
	// The daemon owns canonical state. This closure deliberately does not cancel
	// retained children when a per-turn bridge is discarded.
	return func() error { return nil }, nil
}
