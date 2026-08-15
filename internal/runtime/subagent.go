package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/personas"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/internal/telemetry"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

// SubagentRunner is the runtime-side implementation behind the spawn_agent
// tool. It deliberately runs children synchronously from the parent tool call:
// the parent session is not re-entered, and the child has its own forked
// worktree, conversation log, trace commits, and turn boundary.
type SubagentRunner struct {
	Config *config.Config
	Parent *stadogit.Session

	Provider agent.Provider
	Model    string

	Thinking             string
	ThinkingBudgetTokens int
	ReasoningEffort      string
	System               string
	SystemTemplate       string

	AgentName string
	OnEvent   func(SubagentEvent)

	// InboxFn is the optional pull-source for operator- or peer-
	// agent injected messages, drained at every turn boundary by
	// AgentLoop. Wired by Fleet.runGoroutine for fleet-spawned
	// children so AgentSendMessage actually delivers. Nil for
	// direct-spawn callers (no fleet inbox to drain).
	InboxFn func() []string

	// PersonaName is the parent's active persona; subagents inherit
	// it unless their spawn request specified one. Empty = no
	// inherited persona (use bundled default). EP-0038i.
	PersonaName string

	// QuietRegistryDiagnostics is set by TUI-originated runners so background
	// child registry construction cannot write stderr over the alternate screen.
	QuietRegistryDiagnostics bool
	Metrics                  telemetry.Metrics
	Broker                   BrokerController
	// ResolveSource returns an authorized immutable source session/checkpoint.
	// Nil permits only the active parent.
	ResolveSource func(context.Context, subagent.Source) (*stadogit.Session, error)
	// ResolveProviderModel resolves an explicitly requested configured provider
	// and model. Nil denies changes. Provider credentials and endpoints stay in
	// the native config/secret layer and never enter the child request. A
	// successful call transfers ownership of a fresh provider instance to this
	// runner; it is closed exactly once after the child terminates or validation
	// rejects the resolved profile.
	ResolveProviderModel func(context.Context, string, string) (agent.Provider, string, error)

	// The pinned source fields are host-owned and deliberately absent from
	// subagent.Request. sourcePinned distinguishes an exact empty snapshot from
	// an unpinned request; a zero git hash cannot carry that distinction alone.
	pinnedSource          *stadogit.Session
	pinnedSourceHead      plumbing.Hash
	pinnedConversation    []agent.Message
	pinnedConversationSet bool
	sourcePinned          bool
}

var (
	_ SnapshotSpawner      = SubagentRunner{}
	_ RequestSourceSpawner = SubagentRunner{}
	_ ForkPointSpawner     = SubagentRunner{}
)

// WithInbox returns a copy of the runner with InboxFn set. Implements
// the inbox-aware-spawner contract Fleet.runGoroutine uses to wire
// FleetBridge AgentSendMessage delivery.
func (r SubagentRunner) WithInbox(fn func() []string) Spawner {
	r.InboxFn = fn
	return r
}

// PinSpawnSource implements SnapshotSpawner. Capturing the tree-ref commit
// here, before Fleet launches its goroutine, makes the child worktree match the
// exact state at request time even if the parent continues immediately.
func (r SubagentRunner) PinSpawnSource(_ context.Context) (Spawner, error) {
	if r.Parent == nil || r.Parent.Sidecar == nil {
		return nil, errors.New("spawn_agent: parent session required to pin source")
	}
	head, err := r.Parent.TreeHead()
	if err != nil {
		return nil, fmt.Errorf("spawn_agent: pin parent tree head: %w", err)
	}
	r.pinnedSource = r.Parent
	r.pinnedSourceHead = head
	r.sourcePinned = true
	return r, nil
}

// PinSpawnRequestSource resolves and copies an optional application-selected
// source synchronously. An exact application turn_ref uses the form
// git:refs/sessions/<session>/tree@<commit>#turn-N-iteration-M. Its fragment is
// an audit coordinate; the immutable commit is the source of tree authority.
// Exact turn refs intentionally start with a fresh child conversation so a
// reviewer receives only its bounded review prompt, not mutable worker chat.
func (r SubagentRunner) PinSpawnRequestSource(ctx context.Context, requested *subagent.Source) (Spawner, error) {
	if requested == nil {
		return r.PinSpawnSource(ctx)
	}
	requestedCopy := *requested
	selector := strings.TrimSpace(requestedCopy.At)
	if strings.HasPrefix(selector, "git:") {
		sessionID, parseErr := applicationTurnRefSessionID(selector)
		if parseErr != nil {
			return nil, parseErr
		}
		if requestedCopy.SessionID != "" && requestedCopy.SessionID != sessionID {
			return nil, errors.New("spawn_agent: source.session_id disagrees with exact turn_ref")
		}
		requestedCopy.SessionID = sessionID
	}
	if r.ResolveSource == nil {
		return nil, errors.New("spawn_agent: historical source is not authorized on this surface")
	}
	source, err := r.ResolveSource(ctx, requestedCopy)
	if err != nil {
		return nil, fmt.Errorf("spawn_agent: resolve requested source: %w", err)
	}
	if source == nil || source.Sidecar == nil || source.ID != requestedCopy.SessionID {
		return nil, errors.New("spawn_agent: requested source identity mismatch")
	}

	var head plumbing.Hash
	var seed []agent.Message
	switch {
	case strings.HasPrefix(selector, "git:"):
		head, err = parseApplicationTurnRef(source, selector)
		if err != nil {
			return nil, err
		}
		// Fresh independent reviewers must not inherit later mutable transcript
		// bytes merely because their exact evidence source is a worker session.
		seed = []agent.Message{}
	case strings.HasPrefix(selector, "turns/") || selector == "last_committed_turn":
		if strings.HasPrefix(selector, "turns/") {
			turn, parseErr := strconv.Atoi(strings.TrimPrefix(selector, "turns/"))
			if parseErr != nil || turn < 0 {
				return nil, errors.New("spawn_agent: invalid historical turn selector")
			}
			head, err = source.Sidecar.ResolveRef(stadogit.TurnTagRef(source.ID, turn))
		} else {
			head, err = source.TreeHead()
		}
		if err != nil {
			return nil, fmt.Errorf("spawn_agent: resolve requested source tree: %w", err)
		}
		seed, err = historicalSeed(source, selector)
		if err != nil {
			return nil, fmt.Errorf("spawn_agent: pin requested conversation: %w", err)
		}
	default:
		return nil, errors.New("spawn_agent: source.at must be an exact application turn_ref, turns/N, or last_committed_turn")
	}
	r.pinnedSource = source
	r.pinnedSourceHead = head
	r.pinnedConversation = append([]agent.Message(nil), seed...)
	r.pinnedConversationSet = true
	r.sourcePinned = true
	return r, nil
}

func applicationTurnRefSessionID(value string) (string, error) {
	body := strings.TrimPrefix(strings.TrimSpace(value), "git:")
	anchor, _, ok := strings.Cut(body, "#")
	if !ok {
		return "", errors.New("spawn_agent: exact source requires a turn fragment")
	}
	ref, _, ok := strings.Cut(anchor, "@")
	if !ok {
		return "", errors.New("spawn_agent: exact source requires an immutable tree commit")
	}
	const prefix, suffix = "refs/sessions/", "/tree"
	if !strings.HasPrefix(ref, prefix) || !strings.HasSuffix(ref, suffix) {
		return "", errors.New("spawn_agent: exact source must name a session tree ref")
	}
	sessionID := strings.TrimSuffix(strings.TrimPrefix(ref, prefix), suffix)
	if sessionID == "" || strings.ContainsAny(sessionID, "/\\") {
		return "", errors.New("spawn_agent: invalid session identity in exact source")
	}
	return sessionID, nil
}

func parseApplicationTurnRef(source *stadogit.Session, value string) (plumbing.Hash, error) {
	if source == nil || source.Sidecar == nil {
		return plumbing.ZeroHash, errors.New("spawn_agent: exact source session is unavailable")
	}
	body := strings.TrimPrefix(strings.TrimSpace(value), "git:")
	anchor, fragment, ok := strings.Cut(body, "#")
	if !ok || fragment == "" || strings.Contains(fragment, "#") {
		return plumbing.ZeroHash, errors.New("spawn_agent: exact source requires one turn fragment")
	}
	ref, version, ok := strings.Cut(anchor, "@")
	if !ok || ref != stadogit.TreeRef(source.ID).String() {
		return plumbing.ZeroHash, errors.New("spawn_agent: exact source ref does not match the authorized session tree")
	}
	var turn, iteration int
	if _, err := fmt.Sscanf(fragment, "turn-%d-iteration-%d", &turn, &iteration); err != nil ||
		turn < 1 || iteration < 1 || fragment != fmt.Sprintf("turn-%d-iteration-%d", turn, iteration) {
		return plumbing.ZeroHash, errors.New("spawn_agent: invalid exact source turn fragment")
	}
	// The event producer and application ABI use one canonical spelling. Git
	// object IDs are case-insensitive when decoded, but accepting an alternate
	// spelling here would let producer and consumer disagree about the exact
	// authenticated selector they journal and deduplicate.
	if version != "empty" && version != strings.ToLower(version) {
		return plumbing.ZeroHash, errors.New("spawn_agent: exact source commit must use lowercase hex")
	}
	return validatePinnedCommit(source, "tree", version)
}

// PinSpawnForkPoint implements ForkPointSpawner. The caller has already
// authenticated and resolved the admission source; this method consumes that
// immutable coordinate synchronously and returns a runner that never consults
// the guest's mutable selector during delayed launch or restart.
func (r SubagentRunner) PinSpawnForkPoint(ctx context.Context, point SpawnForkPoint) (Spawner, error) {
	point.SourceSessionID = strings.TrimSpace(point.SourceSessionID)
	if point.SourceSessionID == "" || point.SourceGeneration == 0 || point.CommittedTurn < 0 {
		return nil, errors.New("spawn_agent: incomplete retained fork point")
	}
	if r.Parent == nil || r.Parent.Sidecar == nil {
		return nil, errors.New("spawn_agent: parent session required to pin retained source")
	}

	selector := fmt.Sprintf("turns/%d", point.CommittedTurn)
	source := r.Parent
	if source.ID != point.SourceSessionID {
		if r.ResolveSource == nil {
			return nil, errors.New("spawn_agent: retained historical source is not authorized on this surface")
		}
		var err error
		source, err = r.ResolveSource(ctx, subagent.Source{SessionID: point.SourceSessionID, At: selector})
		if err != nil {
			return nil, fmt.Errorf("spawn_agent: resolve retained source: %w", err)
		}
	}
	if source == nil || source.Sidecar == nil || source.ID != point.SourceSessionID {
		return nil, errors.New("spawn_agent: retained source identity mismatch")
	}

	tree, err := validatePinnedCommit(source, "tree", point.TreeCommit)
	if err != nil {
		return nil, err
	}
	if _, err := validatePinnedCommit(source, "trace", point.TraceCommit); err != nil {
		return nil, err
	}
	wantDigest, err := hex.DecodeString(point.ConversationDigest)
	if err != nil || len(wantDigest) != sha256.Size {
		return nil, errors.New("spawn_agent: invalid retained conversation digest")
	}
	seed, err := conversationAtDigest(source, point.CommittedTurn, point.ConversationDigest)
	if err != nil {
		return nil, err
	}

	r.pinnedSource = source
	r.pinnedSourceHead = tree
	r.pinnedConversation = append([]agent.Message(nil), seed...)
	r.pinnedConversationSet = true
	r.sourcePinned = true
	return r, nil
}

func conversationAtDigest(source *stadogit.Session, turn int, digest string) ([]agent.Message, error) {
	// Current retained admission emits either an explicit turns/N projection or
	// the default last_committed_turn projection. The persisted digest is the
	// authority: accept only bytes that reproduce it, and retain those bytes so
	// later source movement cannot alter launch or restart context.
	for _, selector := range []string{fmt.Sprintf("turns/%d", turn), "last_committed_turn"} {
		seed, err := historicalSeed(source, selector)
		if err != nil {
			return nil, fmt.Errorf("spawn_agent: pin retained conversation: %w", err)
		}
		seedBytes, err := json.Marshal(seed)
		if err != nil {
			return nil, fmt.Errorf("spawn_agent: encode retained conversation: %w", err)
		}
		got := sha256.Sum256(seedBytes)
		if strings.EqualFold(hex.EncodeToString(got[:]), digest) {
			return seed, nil
		}
	}
	return nil, errors.New("spawn_agent: retained conversation changed after admission")
}

func validatePinnedCommit(source *stadogit.Session, label, value string) (plumbing.Hash, error) {
	value = strings.TrimSpace(value)
	if value == "empty" {
		return plumbing.ZeroHash, nil
	}
	if !plumbing.IsHash(value) {
		return plumbing.ZeroHash, fmt.Errorf("spawn_agent: invalid retained %s commit", label)
	}
	hash := plumbing.NewHash(value)
	if _, err := source.Sidecar.Repo().CommitObject(hash); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("spawn_agent: retained %s commit is unavailable: %w", label, err)
	}
	return hash, nil
}

// SubagentEvent is emitted at child lifecycle boundaries so outer
// orchestration surfaces can notify users without parsing tool JSON.
type SubagentEvent struct {
	Phase           string
	AgentID         string
	ParentSession   string
	ChildSession    string
	Worktree        string
	Role            string
	Mode            string
	Execution       string
	Ownership       string
	WriteScope      []string
	MaxTurns        int
	TokenBudget     int
	Status          string
	TimeoutSeconds  int
	ForkTree        string
	TreeRef         string
	TraceRef        string
	ChangedFiles    []string
	ScopeViolations []string
	Terminal        subagent.TerminalMetadata
	Error           string
}

// AdoptionCommand returns a copy-pasteable command for applying child
// changes into the parent session when the event contains adoptable output.
func (ev SubagentEvent) AdoptionCommand() string {
	return subagentAdoptionCommand(ev.ParentSession, ev.ChildSession, ev.ForkTree, ev.ChangedFiles)
}

func subagentAdoptionCommand(parentID, childID, forkTree string, changedFiles []string) string {
	if parentID == "" || childID == "" || len(changedFiles) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("stado session adopt ")
	b.WriteString(parentID)
	b.WriteString(" ")
	b.WriteString(childID)
	if forkTree != "" {
		b.WriteString(" --fork-tree ")
		b.WriteString(forkTree)
	}
	b.WriteString(" --apply")
	return b.String()
}

func (r SubagentRunner) SpawnSubagent(ctx context.Context, req subagent.Request) (subagent.Result, error) {
	req, err := prepareSubagentRequest(req)
	if err != nil {
		return subagent.Result{}, err
	}
	if r.Config == nil {
		return subagent.Result{}, fmt.Errorf("spawn_agent: config required")
	}
	if r.Parent == nil || r.Parent.Sidecar == nil {
		return subagent.Result{}, fmt.Errorf("spawn_agent: parent session required")
	}
	if r.Provider == nil {
		return subagent.Result{}, fmt.Errorf("spawn_agent: provider required")
	}
	source := r.Parent
	sourceTurnCommit := plumbing.ZeroHash
	if r.sourcePinned {
		if r.pinnedSource == nil || r.pinnedSource.Sidecar == nil {
			return subagent.Result{}, errors.New("spawn_agent: pinned source is unavailable")
		}
		source = r.pinnedSource
		sourceTurnCommit = r.pinnedSourceHead
	} else if req.Source != nil {
		sourceTurnCommit = plumbing.ZeroHash
		if r.ResolveSource == nil {
			return subagent.Result{}, errors.New("spawn_agent: historical source is not authorized on this surface")
		}
		source, err = r.ResolveSource(ctx, *req.Source)
		if err != nil {
			return subagent.Result{}, fmt.Errorf("spawn_agent: resolve source: %w", err)
		}
		if source == nil {
			return subagent.Result{}, errors.New("spawn_agent: source resolver returned nil")
		}
		if strings.HasPrefix(req.Source.At, "turns/") {
			turn, parseErr := strconv.Atoi(strings.TrimPrefix(req.Source.At, "turns/"))
			if parseErr != nil || turn < 0 {
				return subagent.Result{}, errors.New("spawn_agent: invalid historical turn selector")
			}
			sourceTurnCommit, err = source.Sidecar.ResolveRef(stadogit.TurnTagRef(source.ID, turn))
			if err != nil {
				return subagent.Result{}, fmt.Errorf("spawn_agent: resolve historical turn: %w", err)
			}
		}
	}
	childProvider, childModel := r.Provider, r.Model
	providerOwned := false
	requestedProvider := req.Provider
	if requestedProvider == "" {
		requestedProvider = r.Provider.Name()
	}
	requestedModel := req.Model
	if requestedModel == "" {
		requestedModel = r.Model
	}
	if requestedProvider != r.Provider.Name() || requestedModel != r.Model {
		if r.ResolveProviderModel == nil {
			return subagent.Result{}, fmt.Errorf("spawn_agent: requested provider/model %q/%q is unavailable", requestedProvider, requestedModel)
		}
		childProvider, childModel, err = r.ResolveProviderModel(ctx, requestedProvider, requestedModel)
		if err != nil {
			return subagent.Result{}, fmt.Errorf("spawn_agent: requested provider/model %q/%q: %w", requestedProvider, requestedModel, err)
		}
		if childProvider == nil {
			return subagent.Result{}, fmt.Errorf("spawn_agent: requested provider/model %q/%q was not resolved exactly", requestedProvider, requestedModel)
		}
		providerOwned = true
		if childModel != requestedModel {
			if closer, ok := childProvider.(io.Closer); ok {
				_ = closer.Close()
			}
			return subagent.Result{}, fmt.Errorf("spawn_agent: requested provider/model %q/%q was not resolved exactly", requestedProvider, requestedModel)
		}
	}
	childThinking := r.Thinking
	if req.Thinking != "" {
		childThinking = req.Thinking
	}
	childThinkingBudget := r.ThinkingBudgetTokens
	if req.ThinkingBudgetTokens > 0 {
		childThinkingBudget = req.ThinkingBudgetTokens
	}
	caps := agent.CapabilitiesForModel(childProvider, childModel)
	if childThinking == "on" && !caps.SupportsThinking {
		cleanupProvider := childProvider
		if providerOwned {
			if closer, ok := cleanupProvider.(io.Closer); ok {
				_ = closer.Close()
			}
		}
		return subagent.Result{}, fmt.Errorf("spawn_agent: requested thinking is unsupported by %s/%s", requestedProvider, childModel)
	}
	childReasoningEffort := r.ReasoningEffort
	if req.ReasoningEffort != "" {
		childReasoningEffort = req.ReasoningEffort
	}
	if childReasoningEffort != "" && !caps.SupportsReasoningEffort {
		if providerOwned {
			if closer, ok := childProvider.(io.Closer); ok {
				_ = closer.Close()
			}
		}
		return subagent.Result{}, fmt.Errorf("spawn_agent: requested reasoning effort is unsupported by %s/%s", requestedProvider, childModel)
	}
	terminal := subagent.TerminalMetadata{UsageComplete: true}
	var cleanupOnce sync.Once
	cleanupProvider := func() {
		cleanupOnce.Do(func() {
			if !providerOwned {
				return
			}
			closer, ok := childProvider.(io.Closer)
			if !ok {
				return
			}
			if closeErr := closer.Close(); closeErr != nil {
				sum := sha256.Sum256([]byte(closeErr.Error()))
				terminal.Cleanup = &subagent.CleanupDiagnostic{
					Kind: "provider_close", Fingerprint: "sha256:" + hex.EncodeToString(sum[:]),
				}
			}
		})
	}
	defer cleanupProvider()
	childBroker := r.Broker
	var child *stadogit.Session
	if r.Broker != nil {
		childBroker, err = r.Broker.CreateSubagent(ctx, BrokerSubagentRequest{
			Role: req.Role, Mode: req.Mode,
			WriteScope: append([]string(nil), req.WriteScope...),
		})
		if err != nil {
			return subagent.Result{}, fmt.Errorf("spawn_agent: broker child session: %w", err)
		}
		defer func() { _ = childBroker.Close() }()
		if reserved := childBroker.Worktree(); reserved != "" {
			worktreeRoot := filepath.Clean(r.Config.WorktreeDir())
			if filepath.Dir(filepath.Clean(reserved)) != worktreeRoot {
				return subagent.Result{}, fmt.Errorf("spawn_agent: broker child worktree %q is outside %q", reserved, worktreeRoot)
			}
			if r.sourcePinned {
				child, err = ForkSessionAtSnapshotWithID(r.Config, source, sourceTurnCommit, filepath.Base(reserved))
			} else if sourceTurnCommit.IsZero() {
				child, err = ForkSessionWithID(r.Config, source, filepath.Base(reserved))
			} else {
				child, err = ForkSessionAtTurnWithID(r.Config, source, sourceTurnCommit, filepath.Base(reserved))
			}
		}
	}
	if child == nil && err == nil {
		if r.sourcePinned && req.ChildSessionID != "" {
			child, err = ForkSessionAtSnapshotWithID(r.Config, source, sourceTurnCommit, req.ChildSessionID)
		} else if r.sourcePinned {
			child, err = ForkSessionAtSnapshot(r.Config, source, sourceTurnCommit)
		} else if !sourceTurnCommit.IsZero() && req.ChildSessionID != "" {
			child, err = ForkSessionAtTurnWithID(r.Config, source, sourceTurnCommit, req.ChildSessionID)
		} else if !sourceTurnCommit.IsZero() {
			child, err = ForkSessionAtTurn(r.Config, source, sourceTurnCommit)
		} else if req.ChildSessionID != "" {
			child, err = ForkSessionWithID(r.Config, source, req.ChildSessionID)
		} else {
			child, err = ForkSession(r.Config, source)
		}
	}
	if err != nil {
		return subagent.Result{}, fmt.Errorf("spawn_agent: fork child session: %w", err)
	}
	baseTree, err := child.CurrentTree()
	if err != nil {
		return subagent.Result{}, fmt.Errorf("spawn_agent: child base tree: %w", err)
	}
	r.emitSubagentEvent(req, child, "started", "running", "")

	agentName := r.AgentName
	if agentName == "" {
		agentName = "stado-subagent"
	}
	_, _ = child.CommitToTrace(stadogit.CommitMeta{
		Tool:     subagent.ToolName,
		ShortArg: req.Role,
		Summary:  trimForSubagentCommit(req.Prompt, 72),
		Model:    childModel,
		Agent:    agentName,
		Turn:     child.Turn(),
	})

	seed := []agent.Message{}
	if r.pinnedConversationSet {
		seed = append(seed, r.pinnedConversation...)
	} else if req.Source != nil {
		seed, err = historicalSeed(source, req.Source.At)
		if err != nil {
			err = fmt.Errorf("spawn_agent: restore historical context: %w", err)
			result := subagentErrorResult(req, child, "", nil, terminal, err)
			r.emitSubagentResultEvent(req, child, result)
			return result, err
		}
	}
	seed = append(seed, agent.Text(agent.RoleUser, renderSubagentPrompt(req)))
	// A historical tree may contain the source conversation sidecar. The child
	// is a new identity, so seed its own bounded restored transcript instead of
	// appending into inherited bytes.
	if err := os.Remove(filepath.Join(child.WorktreePath, ConversationFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		err = fmt.Errorf("spawn_agent: reset child conversation: %w", err)
		result := subagentErrorResult(req, child, "", nil, terminal, err)
		r.emitSubagentResultEvent(req, child, result)
		return result, err
	}
	if err := WriteConversation(child.WorktreePath, seed); err != nil {
		err = fmt.Errorf("spawn_agent: seed child conversation: %w", err)
		result := subagentErrorResult(req, child, "", nil, terminal, err)
		r.emitSubagentResultEvent(req, child, result)
		return result, err
	}

	exec, err := r.buildExecutorWithNarrow(child, agentName, req.NarrowTools, req.ChildToolOwner)
	if err != nil {
		err = fmt.Errorf("spawn_agent: child tools: %w", err)
		result := subagentErrorResult(req, child, "", nil, terminal, err)
		r.emitSubagentResultEvent(req, child, result)
		return result, err
	}
	if childBroker != nil {
		childBroker.Sandbox().Apply(exec)
	}
	childSandbox := ExecutorSandbox{}
	if childBroker != nil {
		childSandbox = childBroker.Sandbox()
	}
	childHost, scopedHost, err := configureSubagentTools(req, exec,
		childSandbox.DefaultSandboxPolicy(child.WorktreePath))
	if err != nil {
		err = fmt.Errorf("spawn_agent: child tools: %w", err)
		result := subagentErrorResult(req, child, "", nil, terminal, err)
		r.emitSubagentResultEvent(req, child, result)
		return result, err
	}

	childCtx, cancel := context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
	defer cancel()

	// Resolve the child's persona. Empty = inherit (caller's flow
	// already populated r.Persona at WithPersona time when relevant).
	// req.Persona overrides r.Persona for this single spawn.
	personaName := req.Persona
	if personaName == "" {
		personaName = r.PersonaName
	}
	var childPersona *personas.Persona
	if personaName != "" {
		resolver := personas.Resolver{CWD: child.WorktreePath, ConfigDir: config.ConfigDir()}
		if p, perr := resolver.Load(personaName); perr == nil {
			childPersona = p
		} else {
			r.emitSubagentEvent(req, child, "warning", "running", "persona "+personaName+": "+perr.Error())
		}
	}
	// EP-0045: the child loop gets loader/session-bounded skill context rooted at
	// its worktree (∪ child persona), so an installed WASM skill application
	// sees the same facts as the parent. Non-fatal on load error.
	childSkills, skErr := EffectiveSkills(child.WorktreePath, childPersona)
	if skErr != nil {
		r.emitSubagentEvent(req, child, "warning", "running", "skills: "+skErr.Error())
	}
	turnUsageReported := false
	childOpts := AgentLoopOptions{
		Provider:                 childProvider,
		Executor:                 exec,
		Config:                   r.Config,
		Metrics:                  r.Metrics,
		Broker:                   childBroker,
		InitialTaint:             ContextTainted,
		DisableVerify:            true,
		DefaultSandboxPolicy:     childSandbox.DefaultSandboxPolicy(child.WorktreePath),
		Model:                    childModel,
		Messages:                 seed,
		MaxTurns:                 req.MaxTurns,
		Thinking:                 childThinking,
		ThinkingBudgetTokens:     childThinkingBudget,
		ReasoningEffort:          childReasoningEffort,
		System:                   r.System,
		SystemTemplate:           r.SystemTemplate,
		Host:                     childHost,
		InboxFn:                  r.InboxFn,
		Persona:                  childPersona,
		Skills:                   childSkills,
		QuietRegistryDiagnostics: r.QuietRegistryDiagnostics,
		TokenCap:                 req.TokenBudget,
		OnEvent: func(event agent.Event) {
			if event.Usage != nil {
				turnUsageReported = true
			}
		},
		OnTurnComplete: func(_ int, _ string, _ []agent.ToolUseBlock, usage agent.Usage, _ time.Duration) {
			if !turnUsageReported || !accumulateSubagentUsage(&terminal.Usage, usage) {
				terminal.UsageComplete = false
			}
			turnUsageReported = false
		},
	}
	text, msgs, err := AgentLoop(childCtx, childOpts)
	cleanupProvider()
	if appendErr := appendSubagentMessages(child.WorktreePath, msgs, len(seed)); appendErr != nil && err == nil {
		err = appendErr
	}
	if err != nil {
		if errors.Is(childCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			result := subagentResult(req, child, text, msgs)
			result.Terminal = terminal
			result.Status = "timeout"
			result.Error = fmt.Sprintf("child timed out after %d second(s)", req.TimeoutSeconds)
			if detailErr := attachWorkerResultDetails(&result, req, child, baseTree, scopedHost); detailErr != nil {
				result.Error += "; " + detailErr.Error()
			}
			r.attachWorkerAdoptionCommand(&result)
			_, _ = child.CommitToTrace(stadogit.CommitMeta{
				Tool:     subagent.ToolName,
				ShortArg: "timeout",
				Summary:  result.Error,
				Model:    r.Model,
				Agent:    agentName,
				Turn:     child.Turn(),
				Error:    err.Error(),
			})
			r.emitSubagentResultEvent(req, child, result)
			return result, nil
		}
		err = fmt.Errorf("spawn_agent: child %s: %w", child.ID, err)
		result := subagentErrorResult(req, child, text, msgs, terminal, err)
		r.emitSubagentResultEvent(req, child, result)
		return result, err
	}

	result := subagentResult(req, child, text, msgs)
	result.Terminal = terminal
	if err := attachWorkerResultDetails(&result, req, child, baseTree, scopedHost); err != nil {
		err = fmt.Errorf("spawn_agent: worker result: %w", err)
		result.Status = "error"
		result.Error = err.Error()
		r.emitSubagentResultEvent(req, child, result)
		return result, err
	}
	r.attachWorkerAdoptionCommand(&result)
	r.emitSubagentResultEvent(req, child, result)
	return result, nil
}

func accumulateSubagentUsage(total *subagent.TokenUsage, next agent.Usage) bool {
	if total == nil || next.InputTokens < 0 || next.OutputTokens < 0 ||
		next.CacheReadTokens < 0 || next.CacheWriteTokens < 0 {
		return false
	}
	add := func(current, delta int) (int, bool) {
		maxInt := int(^uint(0) >> 1)
		if current < 0 || delta < 0 || current > maxInt-delta {
			return 0, false
		}
		return current + delta, true
	}
	var ok bool
	if total.InputTokens, ok = add(total.InputTokens, next.InputTokens); !ok {
		return false
	}
	if total.OutputTokens, ok = add(total.OutputTokens, next.OutputTokens); !ok {
		return false
	}
	if total.CacheReadTokens, ok = add(total.CacheReadTokens, next.CacheReadTokens); !ok {
		return false
	}
	if total.CacheWriteTokens, ok = add(total.CacheWriteTokens, next.CacheWriteTokens); !ok {
		return false
	}
	return true
}

func historicalSeed(source *stadogit.Session, at string) ([]agent.Message, error) {
	messages, err := LoadConversation(source.WorktreePath)
	if err != nil {
		return nil, err
	}
	limitTurns := -1
	if strings.HasPrefix(at, "turns/") {
		limitTurns, err = strconv.Atoi(strings.TrimPrefix(at, "turns/"))
		if err != nil {
			return nil, err
		}
	}
	bounded := make([]agent.Message, 0, min(len(messages), 64))
	turns, bytes := 0, 0
	for _, message := range messages {
		if limitTurns >= 0 && turns >= limitTurns {
			break
		}
		raw, _ := json.Marshal(message)
		if len(bounded) >= 64 || bytes+len(raw) > 128<<10 {
			break
		}
		bounded = append(bounded, message)
		bytes += len(raw)
		if message.Role == agent.RoleAssistant {
			turns++
		}
	}
	return bounded, nil
}

func (r SubagentRunner) buildExecutor(child *stadogit.Session, agentName string) (*tools.Executor, error) {
	return r.buildExecutorWithNarrow(child, agentName, nil, "")
}

func (r SubagentRunner) buildExecutorWithNarrow(child *stadogit.Session, agentName string, narrowTools []string, childToolOwner string) (*tools.Executor, error) {
	exact := make(map[string]bool, len(narrowTools))
	for _, name := range narrowTools {
		if name = strings.TrimSpace(name); name != "" {
			exact[name] = true
		}
	}
	if r.QuietRegistryDiagnostics {
		var exec *tools.Executor
		var err error
		withRegistryDiagnosticsSuppressed(func() {
			exec, err = buildExecutorForSurface(child, r.Config, agentName, r.Metrics, exact, childToolOwner)
		})
		return exec, err
	}
	return buildExecutorForSurface(child, r.Config, agentName, r.Metrics, exact, childToolOwner)
}

func prepareSubagentRequest(req subagent.Request) (subagent.Request, error) {
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Role = strings.TrimSpace(req.Role)
	req.Mode = strings.TrimSpace(req.Mode)
	req.Ownership = strings.TrimSpace(req.Ownership)
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = strings.TrimSpace(req.Model)
	req.Thinking = strings.TrimSpace(req.Thinking)
	req.ReasoningEffort = strings.TrimSpace(req.ReasoningEffort)
	req.ToolProfile = strings.TrimSpace(req.ToolProfile)
	if req.Prompt == "" {
		return subagent.Request{}, fmt.Errorf("spawn_agent: prompt is required")
	}
	req = normalizeSubagentRequest(req)
	if err := subagent.ValidateProviderProfile(req); err != nil {
		return subagent.Request{}, err
	}
	writeScope, err := subagent.NormalizeWriteScope(req.WriteScope)
	if err != nil {
		return subagent.Request{}, fmt.Errorf("spawn_agent: write_scope: %w", err)
	}
	req.WriteScope = writeScope
	switch req.ToolProfile {
	case "", "read_only", "worker_safe", "research":
	default:
		return subagent.Request{}, fmt.Errorf("spawn_agent: unknown tool_profile %q", req.ToolProfile)
	}
	switch {
	case req.Role == subagent.DefaultRole && req.Mode == subagent.DefaultMode:
		return req, nil
	case req.Role == subagent.WorkerRole && req.Mode == subagent.WorkspaceWriteMode:
		if req.Ownership == "" {
			return subagent.Request{}, fmt.Errorf("spawn_agent: ownership is required for %s", subagent.WorkspaceWriteMode)
		}
		if len(req.WriteScope) == 0 {
			return subagent.Request{}, fmt.Errorf("spawn_agent: write_scope is required for %s", subagent.WorkspaceWriteMode)
		}
		return req, nil
	default:
		return subagent.Request{}, fmt.Errorf("spawn_agent: role %q with mode %q is not supported", req.Role, req.Mode)
	}
}

func (r SubagentRunner) emitSubagentEvent(req subagent.Request, child *stadogit.Session, phase, status, errMsg string) {
	r.emitSubagentEventWithTerminal(req, child, phase, status, errMsg, subagent.TerminalMetadata{})
}

func (r SubagentRunner) emitSubagentEventWithTerminal(req subagent.Request, child *stadogit.Session, phase, status, errMsg string, terminal subagent.TerminalMetadata) {
	if r.OnEvent == nil || child == nil {
		return
	}
	parentID := ""
	if r.Parent != nil {
		parentID = r.Parent.ID
	}
	r.OnEvent(SubagentEvent{
		Phase:          phase,
		AgentID:        req.AgentID,
		ParentSession:  parentID,
		ChildSession:   child.ID,
		Worktree:       child.WorktreePath,
		Role:           req.Role,
		Mode:           req.Mode,
		Execution:      req.Execution,
		Ownership:      req.Ownership,
		WriteScope:     append([]string(nil), req.WriteScope...),
		MaxTurns:       req.MaxTurns,
		TokenBudget:    req.TokenBudget,
		Status:         status,
		TimeoutSeconds: req.TimeoutSeconds,
		TreeRef:        subagentTreeEvidenceRef(child),
		TraceRef:       subagentTraceEvidenceRef(child),
		Terminal:       terminal,
		Error:          errMsg,
	})
}

func (r SubagentRunner) emitSubagentResultEvent(req subagent.Request, child *stadogit.Session, result subagent.Result) {
	if r.OnEvent == nil || child == nil {
		return
	}
	parentID := ""
	if r.Parent != nil {
		parentID = r.Parent.ID
	}
	r.OnEvent(SubagentEvent{
		Phase:           "finished",
		AgentID:         req.AgentID,
		ParentSession:   parentID,
		ChildSession:    child.ID,
		Worktree:        child.WorktreePath,
		Role:            req.Role,
		Mode:            req.Mode,
		Execution:       req.Execution,
		Ownership:       req.Ownership,
		WriteScope:      append([]string(nil), req.WriteScope...),
		MaxTurns:        req.MaxTurns,
		TokenBudget:     req.TokenBudget,
		Status:          result.Status,
		TimeoutSeconds:  req.TimeoutSeconds,
		ForkTree:        result.ForkTree,
		TreeRef:         subagentTreeEvidenceRef(child),
		TraceRef:        subagentTraceEvidenceRef(child),
		ChangedFiles:    append([]string(nil), result.ChangedFiles...),
		ScopeViolations: append([]string(nil), result.ScopeViolations...),
		Terminal:        result.Terminal,
		Error:           result.Error,
	})
}

func subagentTreeEvidenceRef(child *stadogit.Session) string {
	if child == nil {
		return ""
	}
	head, err := child.TreeHead()
	if err != nil || head.IsZero() {
		return ""
	}
	return "git:" + stadogit.TreeRef(child.ID).String() + "@" + head.String()
}

func subagentTraceEvidenceRef(child *stadogit.Session) string {
	if child == nil {
		return ""
	}
	head, err := child.TraceHead()
	if err != nil || head.IsZero() {
		return ""
	}
	return "git:" + stadogit.TraceRef(child.ID).String() + "@" + head.String()
}

func (r SubagentRunner) attachWorkerAdoptionCommand(result *subagent.Result) {
	if result == nil || r.Parent == nil {
		return
	}
	result.AdoptionCommand = subagentAdoptionCommand(
		r.Parent.ID,
		result.ChildSession,
		result.ForkTree,
		result.ChangedFiles,
	)
}

func normalizeSubagentRequest(req subagent.Request) subagent.Request {
	if req.Role == "" {
		req.Role = subagent.DefaultRole
	}
	if req.Mode == "" {
		req.Mode = subagent.DefaultMode
	}
	if req.MaxTurns <= 0 {
		req.MaxTurns = subagent.DefaultTurns
	}
	if req.MaxTurns > subagent.MaxTurns {
		req.MaxTurns = subagent.MaxTurns
	}
	if req.TimeoutSeconds <= 0 {
		req.TimeoutSeconds = subagent.DefaultTimeoutSeconds
	}
	if req.TimeoutSeconds > subagent.MaxTimeoutSeconds {
		req.TimeoutSeconds = subagent.MaxTimeoutSeconds
	}
	if req.TokenBudget <= 0 {
		req.TokenBudget = subagent.DefaultTokenBudget
	}
	return req
}

func subagentResult(req subagent.Request, child *stadogit.Session, text string, msgs []agent.Message) subagent.Result {
	return subagent.Result{
		Status:         "completed",
		Role:           req.Role,
		Mode:           req.Mode,
		ChildSession:   child.ID,
		Worktree:       child.WorktreePath,
		Text:           strings.TrimSpace(text),
		MessageCount:   len(msgs),
		TimeoutSeconds: req.TimeoutSeconds,
	}
}

// subagentErrorResult preserves the same host-collected terminal metadata in
// both terminal observation paths: the synchronous Spawner result retained by
// Fleet and the SubagentEvent published as agent.down. Returning a zero result
// with an error would make agent:read report zero-value/incomplete terminal
// facts even though the durable event already carried the measured facts.
func subagentErrorResult(req subagent.Request, child *stadogit.Session, text string, msgs []agent.Message, terminal subagent.TerminalMetadata, err error) subagent.Result {
	result := subagentResult(req, child, text, msgs)
	result.Status = "error"
	result.Terminal = terminal
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func configureSubagentTools(req subagent.Request, exec *tools.Executor, defaultSandboxPolicy any) (tool.Host, *subagent.ScopedWriteHost, error) {
	for _, name := range req.NarrowTools {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := exec.Registry.Get(name); !ok {
			return nil, nil, fmt.Errorf("narrow tool %q is unavailable in the authenticated child registry", name)
		}
	}
	if req.Mode == subagent.WorkspaceWriteMode {
		keepWorkspaceWriteTools(exec.Registry)
		applyToolNarrowing(exec.Registry, req)
		scopedHost, err := subagent.NewScopedWriteHost(autoApproveHost{
			workdir:              exec.Session.WorktreePath,
			readLog:              exec.ReadLog,
			runner:               exec.Runner,
			defaultSandboxPolicy: defaultSandboxPolicy,
		}, req.WriteScope)
		if err != nil {
			return nil, nil, err
		}
		return scopedHost, scopedHost, nil
	}
	keepReadOnlyTools(exec.Registry)
	applyToolNarrowing(exec.Registry, req)
	return nil, nil, nil
}

func applyToolNarrowing(reg *tools.Registry, req subagent.Request) {
	if reg == nil {
		return
	}
	allowed := map[string]bool{}
	for _, name := range req.NarrowTools {
		allowed[strings.TrimSpace(name)] = true
	}
	if len(allowed) == 0 {
		switch req.ToolProfile {
		case "research":
			for _, name := range []string{"fs__read", "fs__glob", "fs__grep", "rg__search", "readctx__read", "lsp__definition", "lsp__references", "lsp__symbols", "lsp__hover"} {
				allowed[name] = true
			}
		case "read_only":
			for _, registered := range reg.All() {
				if reg.ClassOf(registered.Name()) == tool.ClassNonMutating {
					allowed[registered.Name()] = true
				}
			}
		case "worker_safe", "":
			return
		}
	}
	for _, registered := range reg.All() {
		if !allowed[registered.Name()] {
			reg.Unregister(registered.Name())
		}
	}
}

func attachWorkerResultDetails(result *subagent.Result, req subagent.Request, child *stadogit.Session, baseTree plumbing.Hash, scopedHost *subagent.ScopedWriteHost) error {
	if req.Mode != subagent.WorkspaceWriteMode {
		return nil
	}
	result.ForkTree = hashString(baseTree)
	if scopedHost != nil {
		result.ScopeViolations = scopedHost.ScopeViolations()
	}
	currentTree, err := child.CurrentTree()
	if err != nil {
		return err
	}
	changed, err := child.ChangedFilesBetween(baseTree, currentTree)
	if err != nil {
		return err
	}
	result.ChangedFiles = reportableWorkerChangedFiles(changed)
	return nil
}

func reportableWorkerChangedFiles(files []string) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		switch {
		case file == ".stado-pid" || file == ".stado-span-context":
			continue
		case file == ".git" || strings.HasPrefix(file, ".git/"):
			continue
		case file == ".stado" || strings.HasPrefix(file, ".stado/"):
			continue
		default:
			out = append(out, file)
		}
	}
	return out
}

func keepReadOnlyTools(reg *tools.Registry) {
	if reg == nil {
		return
	}
	for _, t := range reg.All() {
		name := t.Name()
		if reg.ClassOf(name) != tool.ClassNonMutating {
			reg.Unregister(name)
		}
	}
}

func keepWorkspaceWriteTools(reg *tools.Registry) {
	if reg == nil {
		return
	}
	// Wire-form names per Step 7 of EP-no-internal-tools. Bare names
	// like "read", "write", "edit", "ripgrep", "find_definition" no
	// longer exist on the registry — they're all <plugin>__<tool>.
	allowed := map[string]struct{}{
		"fs__read":        {},
		"fs__glob":        {},
		"fs__grep":        {},
		"fs__write":       {},
		"fs__edit":        {},
		"rg__search":      {},
		"readctx__read":   {},
		"lsp__definition": {},
		"lsp__references": {},
		"lsp__symbols":    {},
		"lsp__hover":      {},
	}
	for _, t := range reg.All() {
		if _, ok := allowed[t.Name()]; !ok {
			reg.Unregister(t.Name())
		}
	}
}

func appendSubagentMessages(worktree string, msgs []agent.Message, persisted int) error {
	if len(msgs) <= persisted {
		return nil
	}
	_, err := AppendMessagesFrom(worktree, msgs, persisted)
	return err
}

func renderSubagentPrompt(req subagent.Request) string {
	var b strings.Builder
	if req.Mode == subagent.WorkspaceWriteMode {
		b.WriteString("You are a scoped worker agent spawned by a parent stado session.\n")
		b.WriteString("Make only the requested changes and keep your final response concise.\n")
		b.WriteString("You may write only paths inside Write scope. Do not run shell commands or edit files outside scope.\n\n")
	} else {
		b.WriteString("You are a read-only sidecar agent spawned by a parent stado session.\n")
		b.WriteString("Return concise findings for the parent. Include file paths, line numbers, and uncertainties when relevant.\n")
		b.WriteString("Do not edit files, run mutating commands, or make recommendations that depend on changes you did not verify.\n\n")
	}
	b.WriteString("Role: ")
	b.WriteString(req.Role)
	b.WriteString("\nMode: ")
	b.WriteString(req.Mode)
	if req.Ownership != "" {
		b.WriteString("\nOwnership: ")
		b.WriteString(req.Ownership)
	}
	if len(req.WriteScope) > 0 {
		b.WriteString("\nWrite scope:")
		for _, scope := range req.WriteScope {
			b.WriteString("\n- ")
			b.WriteString(scope)
		}
	}
	b.WriteString("\n\nTask:\n")
	b.WriteString(req.Prompt)
	return b.String()
}

func trimForSubagentCommit(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 0 {
		return "..."
	}
	if max <= 3 {
		return string(r[:max])
	}
	return string(r[:max-3]) + "..."
}
