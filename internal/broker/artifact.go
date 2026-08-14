package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/user"
	"strings"
	"sync"
	"time"

	"github.com/foobarto/stado/internal/artifacts"
	"github.com/foobarto/stado/internal/broker/application"
	"github.com/foobarto/stado/internal/broker/authority"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/plugins"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/workdirpath"
)

const maxArtifactRPCBytes = 1 << 20

type artifactBrokerState struct {
	mu                sync.RWMutex
	service           *artifacts.Service
	application       *application.Service
	kinds             *artifacts.KindRegistry
	bindings          map[string]artifactBinding
	lifecycleBindings map[lifecycleBindingKey]string
	store             *wal.Store
	verifier          ArtifactPluginVerifier
}

// ArtifactPluginVerifier reloads plugin identity and signed manifest from a
// broker-controlled trust source. Production must not validate a host-supplied
// identity/manifest pair merely against itself.
type ArtifactPluginVerifier interface {
	VerifyArtifactPlugin(context.Context, plugins.RuntimeIdentity, plugins.Manifest) (plugins.RuntimeIdentity, plugins.Manifest, error)
}

type ArtifactPluginVerifierFunc func(context.Context, plugins.RuntimeIdentity, plugins.Manifest) (plugins.RuntimeIdentity, plugins.Manifest, error)

func (f ArtifactPluginVerifierFunc) VerifyArtifactPlugin(ctx context.Context, identity plugins.RuntimeIdentity, manifest plugins.Manifest) (plugins.RuntimeIdentity, plugins.Manifest, error) {
	return f(ctx, identity, manifest)
}

type artifactBinding struct {
	token             string
	sessionID         string
	generation        uint64
	controllerVersion uint64
	principal         string
	repoID            string
	ancestors         []string
	identity          plugins.RuntimeIdentity
	caps              plugins.ArtifactCapabilities
	capabilities      map[string]struct{}
	eventKinds        []string
	lifecycle         bool
}

type lifecycleBindingKey struct {
	sessionID  string
	generation uint64
	namespace  string
}

// ConfigureArtifactStore installs the canonical broker-owned artifact
// authority. The caller transfers one Store reference to Service; Close must
// be called when the daemon stops. There is deliberately no filesystem
// fallback in the WASM runtime.
func (s *Service) ConfigureArtifactStore(store *wal.Store, verifier ArtifactPluginVerifier) error {
	if store == nil {
		return errors.New("broker: artifact store required")
	}
	if verifier == nil {
		return errors.New("broker: artifact plugin verifier required")
	}
	_, consumer := authority.New(store)
	kinds := artifacts.DefaultKindRegistry()
	state := &artifactBrokerState{
		service:     artifacts.NewServiceWithKinds(store, consumer, kinds),
		application: application.New(store),
		kinds:       kinds, bindings: make(map[string]artifactBinding),
		lifecycleBindings: make(map[lifecycleBindingKey]string), store: store,
		verifier: verifier,
	}
	if s.artifacts != nil {
		return errors.New("broker: artifact store already configured")
	}
	if err := s.configureSessionScopes(store); err != nil {
		return fmt.Errorf("broker: restore durable session scopes: %w", err)
	}
	s.artifacts = state
	return nil
}

// Close releases broker-owned state handles. It is safe to call on policy-only
// services and more than once; wal.Store.Close provides the same idempotent
// shared-handle semantics.
func (s *Service) Close() error {
	if s == nil || s.artifacts == nil || s.artifacts.store == nil {
		return nil
	}
	return s.artifacts.store.Close()
}

func localArtifactPrincipal() string {
	if current, err := user.Current(); err == nil && current.Uid != "" {
		return "os-user:" + current.Uid
	}
	return "local"
}

func canonicalArtifactRepoID(cwd string) (string, error) {
	if strings.TrimSpace(cwd) == "" {
		return "", nil
	}
	return stadogit.RepoID(workdirpath.FindRepoRoot(cwd))
}

func (s *Service) bindArtifacts(ctx context.Context, params ArtifactBindParams) (ArtifactBindResult, error) {
	return s.bindPlugin(ctx, params, false)
}

func (s *Service) bindApplication(ctx context.Context, params ApplicationBindParams) (ArtifactBindResult, error) {
	return s.bindPlugin(ctx, ArtifactBindParams(params), true)
}

func (s *Service) bindPlugin(ctx context.Context, params ArtifactBindParams, lifecycle bool) (ArtifactBindResult, error) {
	state := s.artifacts
	if state == nil || state.service == nil || state.kinds == nil {
		return ArtifactBindResult{}, errors.New("artifact authority unavailable")
	}
	// Authenticate before consulting the plugin trust source. The daemon socket
	// proves only the local OS user; the controller bearer proves that this
	// native caller owns the exact target session.
	s.sessionsMu.RLock()
	session, ok := s.sessions[params.SessionID]
	if !ok {
		s.sessionsMu.RUnlock()
		return ArtifactBindResult{}, ErrSessionNotFound
	}
	if session.terminated {
		s.sessionsMu.RUnlock()
		return ArtifactBindResult{}, ErrSessionTerminated
	}
	if err := authenticateControllerLocked(session, params.ControllerToken); err != nil {
		s.sessionsMu.RUnlock()
		return ArtifactBindResult{}, err
	}
	s.sessionsMu.RUnlock()
	if err := params.Identity.Validate(); err != nil {
		return ArtifactBindResult{}, fmt.Errorf("runtime identity: %w", err)
	}
	verifiedIdentity, verifiedManifest, err := state.verifier.VerifyArtifactPlugin(ctx, params.Identity, params.Manifest)
	if err != nil {
		return ArtifactBindResult{}, fmt.Errorf("broker verify plugin admission: %w", err)
	}
	if verifiedIdentity != params.Identity {
		return ArtifactBindResult{}, errors.New("broker-verified identity differs from requested identity")
	}
	params.Identity, params.Manifest = verifiedIdentity, verifiedManifest
	if err := params.Manifest.ValidateExtensions(); err != nil {
		return ArtifactBindResult{}, fmt.Errorf("manifest: %w", err)
	}
	digest, err := params.Manifest.ManifestDigest()
	if err != nil {
		return ArtifactBindResult{}, err
	}
	if digest != params.Identity.ManifestDigest {
		return ArtifactBindResult{}, errors.New("manifest digest does not match runtime identity")
	}
	caps, err := params.Manifest.ParseArtifactCapabilities()
	if err != nil {
		return ArtifactBindResult{}, err
	}
	if lifecycle && params.Manifest.Lifecycle == nil {
		return ArtifactBindResult{}, errors.New("application binding requires a signed lifecycle manifest")
	}

	s.sessionsMu.RLock()
	session, ok = s.sessions[params.SessionID]
	if !ok {
		s.sessionsMu.RUnlock()
		return ArtifactBindResult{}, ErrSessionNotFound
	}
	if session.terminated {
		s.sessionsMu.RUnlock()
		return ArtifactBindResult{}, ErrSessionTerminated
	}
	if err := authenticateControllerLocked(session, params.ControllerToken); err != nil {
		s.sessionsMu.RUnlock()
		return ArtifactBindResult{}, err
	}
	admissionRequest := CapabilityRequest{
		Purpose: PurposeToolRun, Profile: session.handle.Profile, CWD: session.handle.CWD,
		PluginName: params.Identity.Namespace, SessionID: params.SessionID,
	}
	admission := decisionWithTaint(admissionRequest, s.evaluate(admissionRequest), session.taint)
	if !admission.Admit {
		s.sessionsMu.RUnlock()
		s.logDecision(admissionRequest, admission)
		return ArtifactBindResult{}, fmt.Errorf("plugin admission denied by %s", admission.Rule)
	}
	ancestors := s.artifactAncestorsLocked(session)
	binding := artifactBinding{
		sessionID: params.SessionID, generation: session.generation,
		controllerVersion: session.controllerVersion,
		principal:         session.principal, repoID: session.repoID, ancestors: ancestors,
		identity: params.Identity, caps: caps, capabilities: capabilitySet(params.Manifest.Capabilities),
		lifecycle: lifecycle,
	}
	if params.Manifest.Lifecycle != nil {
		binding.eventKinds = append([]string(nil), params.Manifest.Lifecycle.Events...)
	}
	if err := state.kinds.Register(params.Identity, params.Manifest.ArtifactKinds); err != nil {
		s.sessionsMu.RUnlock()
		return ArtifactBindResult{}, fmt.Errorf("register artifact kinds: %w", err)
	}
	token, err := mintSessionID()
	if err != nil {
		s.sessionsMu.RUnlock()
		return ArtifactBindResult{}, fmt.Errorf("mint artifact binding: %w", err)
	}
	binding.token = "artifact_" + token
	state.mu.Lock()
	if lifecycle {
		key := binding.lifecycleKey()
		if previous := state.lifecycleBindings[key]; previous != "" {
			delete(state.bindings, previous)
		}
		state.lifecycleBindings[key] = binding.token
	}
	state.bindings[binding.token] = binding
	state.mu.Unlock()
	s.sessionsMu.RUnlock()
	s.logDecision(admissionRequest, admission)
	return ArtifactBindResult{
		BindingToken: binding.token, Principal: binding.principal,
		CanonicalRepoID: binding.repoID, SessionID: binding.sessionID,
		SessionGeneration: binding.generation, AncestorSessionIDs: append([]string(nil), binding.ancestors...),
	}, nil
}

func capabilitySet(values []string) map[string]struct{} {
	out := make(map[string]struct{}, len(values))
	for _, value := range values {
		out[value] = struct{}{}
	}
	return out
}

func (b artifactBinding) hasCapability(capability string) bool {
	_, ok := b.capabilities[capability]
	return ok
}

func (b artifactBinding) lifecycleKey() lifecycleBindingKey {
	return lifecycleBindingKey{
		sessionID: b.sessionID, generation: b.generation, namespace: b.identity.Namespace,
	}
}

func (b artifactBinding) applicationAuthority() application.Authority {
	return application.Authority{
		SessionID: b.sessionID, Generation: b.generation,
		PluginID: b.identity.Namespace, Principal: b.principal,
		Actor: "plugin:" + b.identity.Canonical,
	}
}

// artifactAncestorsLocked runs with sessionsMu held for reading or writing.
// A missing historic parent stops the chain; it never broadens visibility.
func (s *Service) artifactAncestorsLocked(session *sessionState) []string {
	var ancestors []string
	seen := map[string]bool{}
	for parentID := session.parentID; parentID != "" && !seen[parentID]; {
		seen[parentID] = true
		ancestors = append(ancestors, parentID)
		parent, ok := s.sessions[parentID]
		if !ok {
			break
		}
		parentID = parent.parentID
	}
	return ancestors
}

func (s *Service) artifactBinding(token string) (artifactBinding, error) {
	state := s.artifacts
	if state == nil {
		return artifactBinding{}, errors.New("artifact authority unavailable")
	}
	state.mu.RLock()
	binding, ok := state.bindings[token]
	state.mu.RUnlock()
	if !ok || token == "" {
		return artifactBinding{}, errors.New("unknown artifact binding")
	}
	s.sessionsMu.RLock()
	session, ok := s.sessions[binding.sessionID]
	active := ok && !session.terminated && session.generation == binding.generation &&
		session.controllerVersion == binding.controllerVersion
	s.sessionsMu.RUnlock()
	if !active {
		return artifactBinding{}, errors.New("stale artifact binding")
	}
	return binding, nil
}

func (s *Service) applicationBinding(token string) (artifactBinding, error) {
	binding, err := s.artifactBinding(token)
	if err != nil {
		return artifactBinding{}, err
	}
	if !binding.lifecycle {
		return artifactBinding{}, errors.New("artifact-only binding cannot call lifecycle application services")
	}
	return binding, nil
}

func (b artifactBinding) queryContext() artifacts.QueryContext {
	return artifacts.QueryContext{
		Principal: b.principal, CanonicalRepoID: b.repoID,
		SessionID: b.sessionID, AncestorSessionIDs: append([]string(nil), b.ancestors...),
	}
}

func (b artifactBinding) actor() string { return "plugin:" + b.identity.Canonical }

type artifactProposeRequest struct {
	Kind         string          `json:"kind"`
	Scope        artifacts.Scope `json:"scope"`
	Tags         []string        `json:"tags,omitempty"`
	Groups       []string        `json:"groups,omitempty"`
	EvidenceRefs []string        `json:"evidence_refs,omitempty"`
	Sensitivity  string          `json:"sensitivity,omitempty"`
	Data         json.RawMessage `json:"data"`
	ExpiresAt    time.Time       `json:"expires_at,omitempty"`
}

type artifactQueryRequest struct {
	Kinds      []string                `json:"kinds"`
	Refs       []artifacts.ArtifactRef `json:"refs,omitempty"`
	Tags       []string                `json:"tags,omitempty"`
	Groups     []string                `json:"groups,omitempty"`
	ActiveOnly bool                    `json:"active_only,omitempty"`
	MaxItems   int                     `json:"max_items,omitempty"`
}

type artifactEditRequest struct {
	Kind            string          `json:"kind"`
	ID              string          `json:"id"`
	ExpectedVersion uint64          `json:"expected_version"`
	Tags            []string        `json:"tags,omitempty"`
	Groups          []string        `json:"groups,omitempty"`
	EvidenceRefs    []string        `json:"evidence_refs,omitempty"`
	Sensitivity     string          `json:"sensitivity,omitempty"`
	Data            json.RawMessage `json:"data"`
	ExpiresAt       time.Time       `json:"expires_at,omitempty"`
}

type artifactObserveRequest struct {
	Kind            string               `json:"kind"`
	ArtifactID      string               `json:"artifact_id"`
	ArtifactVersion uint64               `json:"artifact_version"`
	Event           artifacts.UsageEvent `json:"event"`
	Turn            int                  `json:"turn,omitempty"`
	EvidenceRef     string               `json:"evidence_ref,omitempty"`
}

func (s *Service) artifactPropose(ctx context.Context, params ArtifactCallParams) (json.RawMessage, error) {
	binding, err := s.artifactBinding(params.BindingToken)
	if err != nil {
		return nil, err
	}
	var request artifactProposeRequest
	if err := strictArtifactPayload(params, &request); err != nil {
		return nil, err
	}
	if !binding.caps.AllowsPropose(request.Kind) {
		return nil, fmt.Errorf("artifact propose capability does not allow %q", request.Kind)
	}
	qualified, err := binding.identity.QualifiedKind(request.Kind)
	if err != nil {
		return nil, err
	}
	item := artifacts.Artifact{
		Kind: artifacts.Kind(qualified), Scope: request.Scope,
		Tags: request.Tags, Groups: request.Groups, EvidenceRefs: request.EvidenceRefs,
		Sensitivity: request.Sensitivity, Data: request.Data, ExpiresAt: request.ExpiresAt,
		Provenance: artifacts.Provenance{
			Origins:   []string{"plugin:" + binding.identity.Canonical, "session:" + binding.sessionID},
			CreatedBy: binding.identity.Canonical,
		},
	}
	switch request.Scope {
	case artifacts.ScopeGlobal:
		item.Binding = artifacts.ScopeBinding{Principal: binding.principal}
	case artifacts.ScopeRepo:
		if binding.repoID == "" {
			return nil, errors.New("repository-scoped artifact requires a broker-bound repository")
		}
		item.Binding = artifacts.ScopeBinding{Principal: binding.principal, CanonicalRepoID: binding.repoID}
	case artifacts.ScopeSession:
		item.Binding = artifacts.ScopeBinding{Principal: binding.principal, CanonicalRepoID: binding.repoID, AnchorSessionID: binding.sessionID}
	default:
		return nil, errors.New("invalid artifact scope")
	}
	created, err := s.artifacts.service.Create(ctx, item, binding.principal, binding.actor(), params.RequestID)
	if err != nil {
		return nil, err
	}
	return boundedArtifactResponse(created)
}

func (s *Service) artifactQuery(_ context.Context, params ArtifactCallParams) (json.RawMessage, error) {
	binding, err := s.artifactBinding(params.BindingToken)
	if err != nil {
		return nil, err
	}
	var request artifactQueryRequest
	if err := strictArtifactPayload(params, &request); err != nil {
		return nil, err
	}
	if len(request.Kinds) == 0 || len(request.Kinds) > 32 {
		return nil, errors.New("artifact query requires 1..32 explicit kinds")
	}
	if len(request.Refs) > 32 {
		return nil, errors.New("artifact query refs exceeds 32")
	}
	seenRefs := make(map[string]struct{}, len(request.Refs))
	for _, ref := range request.Refs {
		if strings.TrimSpace(ref.ID) != ref.ID || ref.ID == "" || len(ref.ID) > 256 || ref.Version == 0 {
			return nil, errors.New("artifact query refs require bounded ids and positive versions")
		}
		if _, duplicate := seenRefs[ref.ID]; duplicate {
			return nil, errors.New("artifact query contains duplicate refs")
		}
		seenRefs[ref.ID] = struct{}{}
	}
	kinds := make([]artifacts.Kind, 0, len(request.Kinds))
	for _, kind := range request.Kinds {
		if !binding.caps.AllowsRead(kind) {
			return nil, fmt.Errorf("artifact read capability does not allow %q", kind)
		}
		kinds = append(kinds, artifacts.Kind(kind))
	}
	if request.MaxItems <= 0 {
		request.MaxItems = 16
	}
	if request.MaxItems > 50 {
		return nil, errors.New("artifact query max_items exceeds 50")
	}
	items, err := s.artifacts.service.Query(artifacts.Query{
		Context: binding.queryContext(), Refs: request.Refs, Kinds: kinds, Tags: request.Tags,
		Groups: request.Groups, ActiveOnly: request.ActiveOnly, MaxItems: request.MaxItems,
	})
	if err != nil {
		return nil, err
	}
	return boundedArtifactResponse(struct {
		Items []artifacts.Artifact `json:"items"`
	}{Items: items})
}

func (s *Service) artifactEdit(ctx context.Context, params ArtifactCallParams) (json.RawMessage, error) {
	binding, err := s.artifactBinding(params.BindingToken)
	if err != nil {
		return nil, err
	}
	var request artifactEditRequest
	if err := strictArtifactPayload(params, &request); err != nil {
		return nil, err
	}
	if !binding.caps.AllowsEdit(request.Kind) {
		return nil, fmt.Errorf("artifact edit capability does not allow %q", request.Kind)
	}
	qualified, err := binding.identity.QualifiedKind(request.Kind)
	if err != nil {
		return nil, err
	}
	if !s.artifactVisibleAs(binding, request.ID, artifacts.Kind(qualified), request.ExpectedVersion) {
		return nil, artifacts.ErrNotFound
	}
	replacement := artifacts.Artifact{
		Tags: request.Tags, Groups: request.Groups, EvidenceRefs: request.EvidenceRefs,
		Sensitivity: request.Sensitivity, Data: request.Data, ExpiresAt: request.ExpiresAt,
		Provenance: artifacts.Provenance{
			Origins:   []string{"plugin:" + binding.identity.Canonical, "session:" + binding.sessionID},
			CreatedBy: binding.identity.Canonical, Refs: []string{"artifact:" + request.ID},
		},
	}
	edited, err := s.artifacts.service.Edit(ctx, request.ID, request.ExpectedVersion, replacement, binding.principal, binding.actor(), params.RequestID)
	if err != nil {
		return nil, err
	}
	return boundedArtifactResponse(edited)
}

func (s *Service) artifactObserve(ctx context.Context, params ArtifactCallParams) (json.RawMessage, error) {
	binding, err := s.artifactBinding(params.BindingToken)
	if err != nil {
		return nil, err
	}
	var request artifactObserveRequest
	if err := strictArtifactPayload(params, &request); err != nil {
		return nil, err
	}
	if !binding.caps.AllowsObserve(request.Kind) {
		return nil, fmt.Errorf("artifact observe capability does not allow %q", request.Kind)
	}
	if !s.artifactVisibleAs(binding, request.ArtifactID, artifacts.Kind(request.Kind), request.ArtifactVersion) {
		return nil, artifacts.ErrNotFound
	}
	observation, err := s.artifacts.service.RecordUsage(ctx, artifacts.UsageObservation{
		ArtifactID: request.ArtifactID, ArtifactVersion: request.ArtifactVersion,
		Event: request.Event, SessionID: binding.sessionID, Turn: request.Turn,
		EvidenceRef: request.EvidenceRef,
	}, binding.principal, binding.actor(), params.RequestID)
	if err != nil {
		return nil, err
	}
	return boundedArtifactResponse(observation)
}

func (s *Service) artifactVisibleAs(binding artifactBinding, id string, kind artifacts.Kind, version uint64) bool {
	if strings.TrimSpace(id) == "" || version == 0 {
		return false
	}
	item, ok, err := s.artifacts.service.Visible(id, version, binding.queryContext())
	return err == nil && ok && item.Kind == kind
}

func strictArtifactPayload(params ArtifactCallParams, target any) error {
	if strings.TrimSpace(params.BindingToken) == "" || strings.TrimSpace(params.RequestID) == "" {
		return errors.New("artifact binding_token and request_id are required")
	}
	if len(params.Payload) == 0 || len(params.Payload) > maxArtifactRPCBytes {
		return fmt.Errorf("artifact payload size must be 1..%d bytes", maxArtifactRPCBytes)
	}
	if err := strictUnmarshal(params.Payload, target); err != nil {
		return err
	}
	return nil
}

func boundedArtifactResponse(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxArtifactRPCBytes {
		return nil, errors.New("artifact response exceeds 1 MiB")
	}
	return raw, nil
}
