package main

import (
	"context"
	"fmt"
	"github.com/foobarto/stado/internal/artifacts"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/memory"
	"github.com/foobarto/stado/internal/research"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/trajectory"
	"github.com/foobarto/stado/pkg/agent"
	"path/filepath"
)

func buildRunResearch(cfg *config.Config, provider agent.Provider, sess *stadogit.Session, workdir string) func(context.Context, string, string) (any, error) {
	if cfg == nil || provider == nil || sess == nil {
		return nil
	}
	repoID := ""
	if root := memory.RepoRootFor(workdir); root != "" {
		repoID, _ = stadogit.RepoID(root)
	}
	ancestors, _ := runtime.SessionAncestors(sess.Sidecar, cfg.WorktreeDir(), sess.ID)
	return func(ctx context.Context, kind, query string) (any, error) {
		switch kind {
		case "memory":
			store, err := wal.OpenShared(filepath.Join(cfg.StateDir(), "broker", "events"))
			if err != nil {
				return nil, err
			}
			defer func() { _ = store.Close() }()
			svc := artifacts.NewService(store, nil)
			corpus := research.MemoryCorpus{Service: svc, Context: artifacts.QueryContext{Principal: trajectory.LocalPrincipal(), CanonicalRepoID: repoID, SessionID: sess.ID, AncestorSessionIDs: ancestors}, Kinds: []artifacts.Kind{artifacts.KindMemory, artifacts.KindLesson}}
			return research.Agent{Provider: provider, Model: cfg.Defaults.Model, Corpus: corpus, Kind: "memory"}.Run(ctx, query)
		case "session":
			authorized := map[string]string{}
			for _, id := range append([]string{sess.ID}, ancestors...) {
				if stadogit.ValidateSessionID(id) == nil {
					authorized[id] = filepath.Join(cfg.WorktreeDir(), id)
				}
			}
			return research.Agent{Provider: provider, Model: cfg.Defaults.Model, Corpus: research.SessionCorpus{Authorized: authorized, MaxMessages: 20}, Kind: "session"}.Run(ctx, query)
		default:
			return nil, fmt.Errorf("unknown research kind %q", kind)
		}
	}
}
