package broker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	evidenceSessions  SessionEvidenceSource
	legacyMemory      *legacyMemorySource
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
	subject           string
	ancestors         []string
	identity          plugins.RuntimeIdentity
	caps              plugins.ArtifactCapabilities
	capabilities      map[string]struct{}
	eventKinds        []string
	lifecycle         bool
	legacyMigration   artifacts.LegacyMigrationIdentity
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
	kinds := artifacts.NewKindRegistry()
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
	if s == nil || s.artifacts == nil {
		return nil
	}
	if s.artifacts.legacyMemory != nil && s.artifacts.legacyMemory.root != nil {
		_ = s.artifacts.legacyMemory.root.Close()
	}
	if s.artifacts.store == nil {
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
	return s.bindPlugin(ctx, params.SessionID, params.ControllerToken, params.Identity, params.Manifest, params.ToolName, false)
}

func (s *Service) bindApplication(ctx context.Context, params ApplicationBindParams) (ArtifactBindResult, error) {
	return s.bindPlugin(ctx, params.SessionID, params.ControllerToken, params.Identity, params.Manifest, "", true)
}

func (s *Service) bindPlugin(ctx context.Context, sessionID, controllerToken string, identity plugins.RuntimeIdentity, manifest plugins.Manifest, toolName string, lifecycle bool) (ArtifactBindResult, error) {
	state := s.artifacts
	if state == nil || state.service == nil || state.kinds == nil {
		return ArtifactBindResult{}, errors.New("artifact authority unavailable")
	}
	// Authenticate before consulting the plugin trust source. The daemon socket
	// proves only the local OS user; the controller bearer proves that this
	// native caller owns the exact target session.
	s.sessionsMu.RLock()
	session, ok := s.sessions[sessionID]
	if !ok {
		s.sessionsMu.RUnlock()
		return ArtifactBindResult{}, ErrSessionNotFound
	}
	if session.terminated {
		s.sessionsMu.RUnlock()
		return ArtifactBindResult{}, ErrSessionTerminated
	}
	if err := authenticateControllerLocked(session, controllerToken); err != nil {
		s.sessionsMu.RUnlock()
		return ArtifactBindResult{}, err
	}
	s.sessionsMu.RUnlock()
	if err := identity.Validate(); err != nil {
		return ArtifactBindResult{}, fmt.Errorf("runtime identity: %w", err)
	}
	verifiedIdentity, verifiedManifest, err := state.verifier.VerifyArtifactPlugin(ctx, identity, manifest)
	if err != nil {
		return ArtifactBindResult{}, fmt.Errorf("broker verify plugin admission: %w", err)
	}
	if verifiedIdentity != identity {
		return ArtifactBindResult{}, errors.New("broker-verified identity differs from requested identity")
	}
	identity, manifest = verifiedIdentity, verifiedManifest
	if err := manifest.ValidateExtensions(); err != nil {
		return ArtifactBindResult{}, fmt.Errorf("manifest: %w", err)
	}
	digest, err := manifest.ManifestDigest()
	if err != nil {
		return ArtifactBindResult{}, err
	}
	if digest != identity.ManifestDigest {
		return ArtifactBindResult{}, errors.New("manifest digest does not match runtime identity")
	}
	effectiveManifest := manifest
	if lifecycle {
		if toolName != "" {
			return ArtifactBindResult{}, errors.New("application binding rejects a tool selector")
		}
		if manifest.Lifecycle == nil {
			return ArtifactBindResult{}, errors.New("application binding requires a signed lifecycle manifest")
		}
	} else {
		if strings.TrimSpace(toolName) == "" {
			return ArtifactBindResult{}, errors.New("ordinary plugin binding requires an exact tool_name")
		}
		var selected *plugins.ToolDef
		for i := range manifest.Tools {
			if manifest.Tools[i].Name == toolName {
				selected = &manifest.Tools[i]
				break
			}
		}
		if selected == nil {
			return ArtifactBindResult{}, fmt.Errorf("tool %q is not declared by the broker-verified manifest", toolName)
		}
		effectiveCapabilities, capErr := manifest.EffectiveToolCapabilities(*selected)
		if capErr != nil {
			return ArtifactBindResult{}, fmt.Errorf("tool %q capabilities: %w", toolName, capErr)
		}
		effectiveManifest.Capabilities = effectiveCapabilities
	}
	caps, err := effectiveManifest.ParseArtifactCapabilities()
	if err != nil {
		return ArtifactBindResult{}, err
	}
	caps, err = caps.ResolveSelf(identity)
	if err != nil {
		return ArtifactBindResult{}, fmt.Errorf("resolve artifact self capabilities: %w", err)
	}
	s.sessionsMu.RLock()
	session, ok = s.sessions[sessionID]
	if !ok {
		s.sessionsMu.RUnlock()
		return ArtifactBindResult{}, ErrSessionNotFound
	}
	if session.terminated {
		s.sessionsMu.RUnlock()
		return ArtifactBindResult{}, ErrSessionTerminated
	}
	if err := authenticateControllerLocked(session, controllerToken); err != nil {
		s.sessionsMu.RUnlock()
		return ArtifactBindResult{}, err
	}
	admissionRequest := CapabilityRequest{
		Purpose: PurposeToolRun, Profile: session.handle.Profile, CWD: session.handle.CWD,
		PluginName: identity.Namespace, SessionID: sessionID,
	}
	admission := s.evaluate(admissionRequest)
	if !admission.Admit {
		s.sessionsMu.RUnlock()
		s.logDecision(admissionRequest, admission)
		return ArtifactBindResult{}, fmt.Errorf("plugin admission denied by %s", admission.Rule)
	}
	ancestors := s.artifactAncestorsLocked(session)
	binding := artifactBinding{
		sessionID: sessionID, generation: session.generation,
		controllerVersion: session.controllerVersion,
		principal:         session.principal, repoID: session.repoID, subject: session.scope.subject, ancestors: ancestors,
		identity: identity, caps: caps, capabilities: capabilitySet(effectiveManifest.Capabilities),
		lifecycle: lifecycle,
	}
	if lifecycle && manifest.Lifecycle != nil {
		binding.eventKinds = append([]string(nil), manifest.Lifecycle.Events...)
	}
	if err := state.kinds.Register(identity, manifest.ArtifactKinds); err != nil {
		s.sessionsMu.RUnlock()
		return ArtifactBindResult{}, fmt.Errorf("register artifact kinds: %w", err)
	}
	if caps.MigrateLegacyMemoryV1 {
		memoryKind, kindErr := identity.QualifiedKind("memory")
		if kindErr != nil {
			s.sessionsMu.RUnlock()
			return ArtifactBindResult{}, kindErr
		}
		lessonKind, kindErr := identity.QualifiedKind("lesson")
		if kindErr != nil {
			s.sessionsMu.RUnlock()
			return ArtifactBindResult{}, kindErr
		}
		memoryDescriptor, memoryOK := state.kinds.Lookup(artifacts.Kind(memoryKind))
		lessonDescriptor, lessonOK := state.kinds.Lookup(artifacts.Kind(lessonKind))
		if !memoryOK || !lessonOK {
			s.sessionsMu.RUnlock()
			return ArtifactBindResult{}, errors.New("legacy memory migration descriptors are unavailable")
		}
		binding.legacyMigration = artifacts.LegacyMigrationIdentity{Runtime: identity, Memory: memoryDescriptor, Lesson: lessonDescriptor}
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
		Actor: "plugin:" + b.identity.Canonical, Subject: b.subject,
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
	Kind               string          `json:"kind"`
	Scope              artifacts.Scope `json:"scope"`
	Tags               []string        `json:"tags,omitempty"`
	Groups             []string        `json:"groups,omitempty"`
	EvidenceRefs       []string        `json:"evidence_refs,omitempty"`
	EvidenceReceiptIDs []string        `json:"evidence_receipt_ids,omitempty"`
	Sensitivity        string          `json:"sensitivity,omitempty"`
	Data               json.RawMessage `json:"data"`
	ExpiresAt          time.Time       `json:"expires_at,omitempty"`
}

type artifactQueryRequest struct {
	Kinds      []string                `json:"kinds"`
	Refs       []artifacts.ArtifactRef `json:"refs,omitempty"`
	Tags       []string                `json:"tags,omitempty"`
	Groups     []string                `json:"groups,omitempty"`
	ActiveOnly bool                    `json:"active_only,omitempty"`
	MaxItems   int                     `json:"max_items,omitempty"`
	PageOffset int                     `json:"page_offset,omitempty"`
	PageDigest string                  `json:"page_digest,omitempty"`
}

type artifactEditRequest struct {
	Kind               string          `json:"kind"`
	ID                 string          `json:"id"`
	ExpectedVersion    uint64          `json:"expected_version"`
	Tags               []string        `json:"tags,omitempty"`
	Groups             []string        `json:"groups,omitempty"`
	EvidenceRefs       []string        `json:"evidence_refs,omitempty"`
	EvidenceReceiptIDs []string        `json:"evidence_receipt_ids,omitempty"`
	Sensitivity        string          `json:"sensitivity,omitempty"`
	Data               json.RawMessage `json:"data"`
	ExpiresAt          time.Time       `json:"expires_at,omitempty"`
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
	derivedEvidence, err := s.resolveArtifactEvidenceReceipts(binding, request.EvidenceReceiptIDs)
	if err != nil {
		return nil, err
	}
	if hasReservedEvidenceRef(request.EvidenceRefs) {
		return nil, errors.New("reserved broker evidence receipt namespace cannot be supplied as a free-form reference")
	}
	item := artifacts.Artifact{
		Kind: artifacts.Kind(qualified), Scope: request.Scope,
		Tags: request.Tags, Groups: request.Groups, EvidenceRefs: append(append([]string(nil), request.EvidenceRefs...), derivedEvidence...),
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
	idempotencyKey, err := artifactMutationIdempotencyKey(binding, "propose", params.RequestID, item.Scope, item.Binding)
	if err != nil {
		return nil, err
	}
	created, err := s.artifacts.service.Create(ctx, item, binding.principal, binding.actor(), idempotencyKey)
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
	if request.PageOffset < 0 || request.PageOffset > 1_000_000 {
		return nil, errors.New("artifact query page_offset is outside 0..1000000")
	}
	if request.PageOffset > 0 && request.PageDigest == "" {
		return nil, errors.New("artifact query page_digest is required after the first page")
	}
	if request.PageDigest != "" && !validSHA256Digest(request.PageDigest) {
		return nil, errors.New("artifact query page_digest is invalid")
	}
	page, err := s.artifacts.service.QueryPage(artifacts.Query{
		Context: binding.queryContext(), Refs: request.Refs, Kinds: kinds, Tags: request.Tags,
		Groups: request.Groups, ActiveOnly: request.ActiveOnly, ExcludeSecret: true, MaxItems: request.MaxItems,
	}, request.PageOffset, request.MaxItems)
	if err != nil {
		return nil, err
	}
	if request.PageDigest != "" && request.PageDigest != page.Digest {
		return nil, errors.New("artifact query projection changed; restart pagination from offset zero")
	}
	return boundedArtifactResponse(struct {
		Items      []artifacts.Artifact `json:"items"`
		PageDigest string               `json:"page_digest"`
		NextOffset int                  `json:"next_offset,omitempty"`
		Complete   bool                 `json:"complete"`
	}{Items: page.Items, PageDigest: page.Digest, NextOffset: page.NextOffset, Complete: page.Complete})
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
	current, visible, err := s.artifactCurrentVisibleAs(binding, request.ID, artifacts.Kind(qualified))
	if err != nil {
		return nil, err
	}
	if !visible {
		return nil, artifacts.ErrNotFound
	}
	derivedEvidence, err := s.resolveArtifactEvidenceReceipts(binding, request.EvidenceReceiptIDs)
	if err != nil {
		return nil, err
	}
	if hasReservedEvidenceRef(request.EvidenceRefs) {
		return nil, errors.New("reserved broker evidence receipt namespace cannot be supplied as a free-form reference")
	}
	replacement := artifacts.Artifact{
		Tags: request.Tags, Groups: request.Groups, EvidenceRefs: append(append([]string(nil), request.EvidenceRefs...), derivedEvidence...),
		Sensitivity: request.Sensitivity, Data: request.Data, ExpiresAt: request.ExpiresAt,
		Provenance: artifacts.Provenance{
			Origins:   []string{"plugin:" + binding.identity.Canonical, "session:" + binding.sessionID},
			CreatedBy: binding.identity.Canonical, Refs: []string{"artifact:" + request.ID},
		},
	}
	idempotencyKey, err := artifactMutationIdempotencyKey(binding, "edit", params.RequestID, current.Scope, current.Binding)
	if err != nil {
		return nil, err
	}
	edited, err := s.artifacts.service.Edit(ctx, request.ID, request.ExpectedVersion, replacement, binding.principal, binding.actor(), idempotencyKey)
	if err != nil {
		return nil, err
	}
	return boundedArtifactResponse(edited)
}

func hasReservedEvidenceRef(refs []string) bool {
	for _, ref := range refs {
		if strings.HasPrefix(ref, "broker:evidence-receipt:") {
			return true
		}
	}
	return false
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

func (s *Service) artifactCurrentVisibleAs(binding artifactBinding, id string, kind artifacts.Kind) (artifacts.Artifact, bool, error) {
	if strings.TrimSpace(id) == "" {
		return artifacts.Artifact{}, false, nil
	}
	item, ok, err := s.artifacts.service.Show(id)
	if err != nil || !ok || item.Kind != kind {
		return artifacts.Artifact{}, false, err
	}
	visible, ok, err := s.artifacts.service.Visible(id, item.Version, binding.queryContext())
	if err != nil || !ok || visible.Kind != kind {
		return artifacts.Artifact{}, false, err
	}
	return visible, true, nil
}

func artifactMutationIdempotencyKey(binding artifactBinding, operation, logical string, scope artifacts.Scope, scopeBinding artifacts.ScopeBinding) (string, error) {
	if strings.TrimSpace(logical) != logical || logical == "" || len(logical) > 256 {
		return "", errors.New("artifact request_id must be a non-empty exact value of at most 256 bytes")
	}
	namespace := struct {
		Principal string                 `json:"principal"`
		Plugin    string                 `json:"plugin"`
		Operation string                 `json:"operation"`
		Scope     artifacts.Scope        `json:"scope"`
		Binding   artifacts.ScopeBinding `json:"binding"`
		Logical   string                 `json:"logical"`
	}{
		Principal: binding.principal, Plugin: binding.identity.Namespace, Operation: operation,
		Scope: scope, Binding: scopeBinding, Logical: logical,
	}
	raw, err := json.Marshal(namespace)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "artifact:" + operation + ":" + hex.EncodeToString(sum[:]), nil
}

func validSHA256Digest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func strictArtifactPayload(params ArtifactCallParams, target any) error {
	if strings.TrimSpace(params.BindingToken) == "" || strings.TrimSpace(params.RequestID) == "" {
		return errors.New("artifact binding_token and request_id are required")
	}
	if strings.TrimSpace(params.RequestID) != params.RequestID || len(params.RequestID) > 256 {
		return errors.New("artifact request_id must be an exact value of at most 256 bytes")
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
