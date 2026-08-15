package plugins

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	maxArtifactKinds           = 32
	maxArtifactKindSchemaBytes = 256 << 10
	maxArtifactKindProjections = 32
	maxArtifactSchemaDepth     = 32
	maxArtifactSchemaNodes     = 4096
	maxArtifactSchemaRegex     = 4096
	maxLifecyclePoints         = 16
	maxLifecycleEvents         = 64
	maxApplicationCommands     = 32
	maximumCommandTimeoutMS    = 15 * 60 * 1000
	defaultLifecycleTimeoutMS  = 5000
	maximumLifecycleTimeoutMS  = 60000
)

var artifactKindNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// ArtifactCapabilities is the broker-consumable projection of the signed
// manifest capability list. Propose/edit are always scoped to local kinds the
// same manifest declares. Read/observe use qualified kind names (or an
// explicit trailing-prefix wildcard) because they may cross plugin ownership.
// Keeping this parser in plugins makes the WASM import gate and the broker
// authority consume one contract instead of reinterpreting strings twice.
type ArtifactCapabilities struct {
	Propose               []string
	Read                  []string
	Edit                  []string
	Observe               []string
	MigrateLegacyMemoryV1 bool
}

// ArtifactKindDef is the signed manifest declaration for one plugin-owned
// artifact data shape. The broker owns the common artifact envelope; Schema
// validates only its dynamic data object. Index contains deterministic JSON
// Pointer projections and never executes plugin code during index rebuild.
type ArtifactKindDef struct {
	Name   string                    `json:"name"`
	Schema string                    `json:"schema"`
	Index  []ArtifactIndexProjection `json:"index,omitempty"`
}

type ArtifactIndexProjection struct {
	Pointer string `json:"pointer"`
	Role    string `json:"role"`
}

// LifecycleDef declares the synchronous hook points and durable broker events
// a persistent WASM application wants to receive. Operator policy may narrow
// this declaration; a manifest can never widen the host's effective policy.
type LifecycleDef struct {
	Points    []string `json:"points,omitempty"`
	Events    []string `json:"events,omitempty"`
	Failure   string   `json:"failure,omitempty"`
	TimeoutMS int      `json:"timeout_ms,omitempty"`
}

// LifecycleCapabilities is the signed, broker-consumable lifecycle authority
// projection. Observe authorizes delivery of a point or event; Decide is a
// narrower set of synchronous points at which the application may deny or
// mutate. Contribute authorizes only the bounded append-only pre_llm context
// contribution defined by EP-0060/0064; it cannot deny or replace host fields.
// Merely subscribing in LifecycleDef never grants any of these authorities.
type LifecycleCapabilities struct {
	Observe    []string
	Decide     []string
	Contribute []string
}

func (m Manifest) ValidateExtensions() error {
	toolNames := make(map[string]bool, len(m.Tools))
	for i, definition := range m.Tools {
		if toolNames[definition.Name] {
			return fmt.Errorf("tools[%d] %q: duplicate tool name", i, definition.Name)
		}
		toolNames[definition.Name] = true
		if definition.ApplicationWorker != nil && m.Lifecycle == nil {
			return fmt.Errorf("tools[%d] %q: application_worker requires a lifecycle declaration", i, definition.Name)
		}
		if definition.ApplicationSession != nil && m.Lifecycle == nil {
			return fmt.Errorf("tools[%d] %q: application_session requires a lifecycle declaration", i, definition.Name)
		}
		if definition.ApplicationWorker != nil && definition.ApplicationSession != nil {
			return fmt.Errorf("tools[%d] %q: application_worker and application_session are mutually exclusive", i, definition.Name)
		}
		if definition.ApplicationSession != nil && definition.AgentChildOnly {
			return fmt.Errorf("tools[%d] %q: application_session and agent_child_only are mutually exclusive", i, definition.Name)
		}
		if m.Lifecycle != nil {
			if definition.AgentChildOnly {
				return fmt.Errorf("tools[%d] %q: agent_child_only is not valid for lifecycle application tools", i, definition.Name)
			}
			if definition.Capabilities != nil {
				return fmt.Errorf("tools[%d] %q: lifecycle application tools must omit capabilities because the persistent module has package-wide authority", i, definition.Name)
			}
		} else if definition.Capabilities == nil {
			return fmt.Errorf("tools[%d] %q: ordinary tools must explicitly declare capabilities, including [] for zero authority", i, definition.Name)
		}
		if _, err := m.EffectiveToolCapabilities(definition); err != nil {
			return fmt.Errorf("tools[%d] %q: %w", i, definition.Name, err)
		}
	}
	if err := validateCommandDefs(m.Commands); err != nil {
		return err
	}
	if len(m.ArtifactKinds) > maxArtifactKinds {
		return fmt.Errorf("too many artifact kinds: %d > %d", len(m.ArtifactKinds), maxArtifactKinds)
	}
	seen := make(map[string]bool, len(m.ArtifactKinds))
	for i, def := range m.ArtifactKinds {
		if err := def.Validate(); err != nil {
			return fmt.Errorf("artifact_kinds[%d]: %w", i, err)
		}
		if seen[def.Name] {
			return fmt.Errorf("duplicate artifact kind %q", def.Name)
		}
		seen[def.Name] = true
	}
	if m.Lifecycle != nil {
		if err := m.Lifecycle.Validate(); err != nil {
			return fmt.Errorf("lifecycle: %w", err)
		}
	}
	if _, err := m.ParseLifecycleCapabilities(); err != nil {
		return err
	}
	if _, err := m.ParseArtifactCapabilities(); err != nil {
		return err
	}
	return nil
}

// EffectiveToolCapabilities returns the exact host-capability view for one
// signed tool definition. Ordinary tools require a present list, which may
// only attenuate package authority. Lifecycle tools must omit it and inherit
// package authority because their callbacks/tools share a persistent Host.
// Callers must keep
// identity, signature, and module instantiation bound to the original manifest
// and use this projection only for the selected tool's Host and risk class.
func (m Manifest) EffectiveToolCapabilities(definition ToolDef) ([]string, error) {
	if m.Lifecycle != nil {
		if definition.Capabilities != nil {
			return nil, errors.New("lifecycle application tool capabilities must be omitted")
		}
		return append([]string(nil), m.Capabilities...), nil
	}
	if definition.Capabilities == nil {
		return nil, errors.New("ordinary tool capabilities must be explicitly declared")
	}
	declaredCapabilities := *definition.Capabilities

	packageCapabilities := make(map[string]struct{}, len(m.Capabilities))
	for _, capability := range m.Capabilities {
		packageCapabilities[capability] = struct{}{}
	}
	seen := make(map[string]struct{}, len(declaredCapabilities))
	for _, capability := range declaredCapabilities {
		if capability == "" || strings.TrimSpace(capability) != capability {
			return nil, fmt.Errorf("tool capability must be a non-empty exact value: %q", capability)
		}
		if _, duplicate := seen[capability]; duplicate {
			return nil, fmt.Errorf("duplicate tool capability %q", capability)
		}
		seen[capability] = struct{}{}
		if _, declared := packageCapabilities[capability]; !declared {
			return nil, fmt.Errorf("tool capability %q is not declared by the package", capability)
		}
	}
	return append([]string(nil), declaredCapabilities...), nil
}

func validateCommandDefs(commands []CommandDef) error {
	if len(commands) > maxApplicationCommands {
		return fmt.Errorf("too many application commands: %d > %d", len(commands), maxApplicationCommands)
	}
	seen := map[string]bool{}
	for i, command := range commands {
		if !validCommandName(command.Name) {
			return fmt.Errorf("commands[%d]: invalid command name %q", i, command.Name)
		}
		if seen[command.Name] {
			return fmt.Errorf("duplicate application command %q", command.Name)
		}
		seen[command.Name] = true
		if strings.TrimSpace(command.Description) == "" || len(command.Description) > 1024 {
			return fmt.Errorf("commands[%d]: description must be 1..1024 bytes", i)
		}
		if len(command.Usage) > 1024 || strings.ContainsAny(command.Usage, "\r\n") {
			return fmt.Errorf("commands[%d]: usage must be a single line of at most 1024 bytes", i)
		}
		if command.TimeoutMS < 0 || command.TimeoutMS > maximumCommandTimeoutMS {
			return fmt.Errorf("commands[%d]: timeout_ms outside 0..%d", i, maximumCommandTimeoutMS)
		}
	}
	return nil
}

// EffectiveTimeoutMS returns the signed command-specific timeout or the
// lifecycle callback timeout when no override was declared. Callers pass the
// already-defaulted lifecycle value, so zero can never escape into execution.
func (c CommandDef) EffectiveTimeoutMS(lifecycleTimeoutMS int) int {
	if c.TimeoutMS == 0 {
		return lifecycleTimeoutMS
	}
	return c.TimeoutMS
}

func validCommandName(value string) bool {
	if value == "" || len(value) > 63 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

func (m Manifest) ParseLifecycleCapabilities() (LifecycleCapabilities, error) {
	var out LifecycleCapabilities
	points, events := map[string]bool{}, map[string]bool{}
	if m.Lifecycle != nil {
		for _, point := range m.Lifecycle.Points {
			points[point] = true
		}
		for _, event := range m.Lifecycle.Events {
			events[event] = true
		}
	}
	seen := map[string]bool{}
	for _, capability := range m.Capabilities {
		if !strings.HasPrefix(capability, "lifecycle:") {
			continue
		}
		parts := strings.SplitN(capability, ":", 3)
		if len(parts) != 3 || parts[2] == "" || strings.TrimSpace(parts[2]) != parts[2] {
			return LifecycleCapabilities{}, fmt.Errorf("invalid lifecycle capability %q", capability)
		}
		if seen[capability] {
			return LifecycleCapabilities{}, fmt.Errorf("duplicate lifecycle capability %q", capability)
		}
		seen[capability] = true
		target := parts[2]
		switch parts[1] {
		case "observe":
			if !points[target] && !events[target] {
				return LifecycleCapabilities{}, fmt.Errorf("lifecycle observe target %q is not subscribed", target)
			}
			out.Observe = append(out.Observe, target)
		case "decide":
			if !points[target] {
				return LifecycleCapabilities{}, fmt.Errorf("lifecycle decide target %q is not a subscribed point", target)
			}
			out.Decide = append(out.Decide, target)
		case "contribute":
			if target != "pre_llm" || !points[target] {
				return LifecycleCapabilities{}, fmt.Errorf("lifecycle contribute target %q is not the subscribed pre_llm point", target)
			}
			out.Contribute = append(out.Contribute, target)
		default:
			return LifecycleCapabilities{}, fmt.Errorf("unknown lifecycle capability operation %q", parts[1])
		}
	}
	if m.Lifecycle != nil {
		for _, point := range m.Lifecycle.Points {
			if !containsCapability(out.Observe, point) {
				return LifecycleCapabilities{}, fmt.Errorf("lifecycle point %q lacks lifecycle:observe capability", point)
			}
		}
		for _, event := range m.Lifecycle.Events {
			if !containsCapability(out.Observe, event) {
				return LifecycleCapabilities{}, fmt.Errorf("lifecycle event %q lacks lifecycle:observe capability", event)
			}
		}
	}
	return out, nil
}

func (c LifecycleCapabilities) CanObserve(target string) bool {
	return containsCapability(c.Observe, target)
}

func (c LifecycleCapabilities) CanDecide(point string) bool {
	return containsCapability(c.Decide, point)
}

func (c LifecycleCapabilities) CanContribute(point string) bool {
	return containsCapability(c.Contribute, point)
}

func (m Manifest) ParseArtifactCapabilities() (ArtifactCapabilities, error) {
	var out ArtifactCapabilities
	declared := make(map[string]bool, len(m.ArtifactKinds))
	for _, definition := range m.ArtifactKinds {
		declared[definition.Name] = true
	}
	seen := map[string]bool{}
	for _, capability := range m.Capabilities {
		if !strings.HasPrefix(capability, "artifact:") {
			continue
		}
		parts := strings.SplitN(capability, ":", 3)
		if len(parts) != 3 || strings.TrimSpace(parts[2]) != parts[2] || parts[2] == "" {
			return ArtifactCapabilities{}, fmt.Errorf("invalid artifact capability %q", capability)
		}
		if seen[capability] {
			return ArtifactCapabilities{}, fmt.Errorf("duplicate artifact capability %q", capability)
		}
		seen[capability] = true
		scope := parts[2]
		switch parts[1] {
		case "propose":
			if !declared[scope] {
				return ArtifactCapabilities{}, fmt.Errorf("artifact propose kind %q is not declared", scope)
			}
			out.Propose = append(out.Propose, scope)
		case "edit":
			if !declared[scope] {
				return ArtifactCapabilities{}, fmt.Errorf("artifact edit kind %q is not declared", scope)
			}
			out.Edit = append(out.Edit, scope)
		case "read", "observe":
			if strings.HasPrefix(scope, "self#") {
				local := strings.TrimPrefix(scope, "self#")
				if !declared[local] || !artifactKindNameRE.MatchString(local) {
					return ArtifactCapabilities{}, fmt.Errorf("artifact %s self kind %q is not declared", parts[1], local)
				}
			} else if !validQualifiedArtifactPattern(scope) {
				return ArtifactCapabilities{}, fmt.Errorf("invalid artifact %s kind pattern %q", parts[1], scope)
			}
			if parts[1] == "read" {
				out.Read = append(out.Read, scope)
			} else {
				out.Observe = append(out.Observe, scope)
			}
		case "migrate":
			if scope != "legacy-memory-v1" {
				return ArtifactCapabilities{}, fmt.Errorf("unknown artifact migration %q", scope)
			}
			if m.Lifecycle == nil {
				return ArtifactCapabilities{}, errors.New("legacy memory migration is lifecycle-application-only")
			}
			if !declared["memory"] || !declared["lesson"] {
				return ArtifactCapabilities{}, errors.New("legacy memory migration requires declared memory and lesson kinds")
			}
			out.MigrateLegacyMemoryV1 = true
		default:
			return ArtifactCapabilities{}, fmt.Errorf("unknown artifact capability operation %q", parts[1])
		}
	}
	return out, nil
}

func validQualifiedArtifactPattern(pattern string) bool {
	if pattern == "*" {
		return true
	}
	if strings.Count(pattern, "*") > 1 || (strings.Contains(pattern, "*") && !strings.HasSuffix(pattern, "*")) {
		return false
	}
	base := strings.TrimSuffix(pattern, "*")
	hash := strings.LastIndexByte(base, '#')
	if hash <= 0 {
		return false
	}
	local := base[hash+1:]
	if strings.HasSuffix(pattern, "*") {
		return local == "" || artifactKindNameRE.MatchString(strings.TrimSuffix(local, "-"))
	}
	return artifactKindNameRE.MatchString(local)
}

func (c ArtifactCapabilities) AllowsPropose(local string) bool {
	return containsCapability(c.Propose, local)
}

// ResolveSelf binds signed self-kind grants to the broker-authenticated
// runtime identity. The guest never supplies or learns authority through this
// conversion: only an exact manifest-declared local kind may use self# and the
// verified loader/broker identity supplies the namespace (EP-0063, EP-0066).
func (c ArtifactCapabilities) ResolveSelf(identity RuntimeIdentity) (ArtifactCapabilities, error) {
	if err := identity.Validate(); err != nil {
		return ArtifactCapabilities{}, err
	}
	resolve := func(patterns []string) ([]string, error) {
		out := make([]string, 0, len(patterns))
		for _, pattern := range patterns {
			if !strings.HasPrefix(pattern, "self#") {
				out = append(out, pattern)
				continue
			}
			qualified, err := identity.QualifiedKind(strings.TrimPrefix(pattern, "self#"))
			if err != nil {
				return nil, err
			}
			out = append(out, qualified)
		}
		return out, nil
	}
	var err error
	out := c
	out.Propose = append([]string(nil), c.Propose...)
	out.Edit = append([]string(nil), c.Edit...)
	if out.Read, err = resolve(c.Read); err != nil {
		return ArtifactCapabilities{}, err
	}
	if out.Observe, err = resolve(c.Observe); err != nil {
		return ArtifactCapabilities{}, err
	}
	return out, nil
}
func (c ArtifactCapabilities) AllowsEdit(local string) bool { return containsCapability(c.Edit, local) }
func (c ArtifactCapabilities) AllowsRead(kind string) bool  { return allowsQualified(c.Read, kind) }
func (c ArtifactCapabilities) AllowsObserve(kind string) bool {
	return allowsQualified(c.Observe, kind)
}

func containsCapability(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func allowsQualified(patterns []string, kind string) bool {
	for _, pattern := range patterns {
		if pattern == "*" || pattern == kind || (strings.HasSuffix(pattern, "*") && strings.HasPrefix(kind, strings.TrimSuffix(pattern, "*"))) {
			return true
		}
	}
	return false
}

func (d ArtifactKindDef) Validate() error {
	if !artifactKindNameRE.MatchString(d.Name) {
		return fmt.Errorf("invalid local kind name %q", d.Name)
	}
	if len(d.Schema) == 0 || len(d.Schema) > maxArtifactKindSchemaBytes {
		return fmt.Errorf("schema size must be 1..%d bytes", maxArtifactKindSchemaBytes)
	}
	compiled, err := compileArtifactSchema(d.Name, d.Schema)
	if err != nil {
		return err
	}
	_ = compiled
	if len(d.Index) > maxArtifactKindProjections {
		return fmt.Errorf("too many index projections: %d > %d", len(d.Index), maxArtifactKindProjections)
	}
	seen := make(map[string]bool, len(d.Index))
	for i, p := range d.Index {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("index[%d]: %w", i, err)
		}
		key := p.Role + "\x00" + p.Pointer
		if seen[key] {
			return fmt.Errorf("duplicate %s projection %q", p.Role, p.Pointer)
		}
		seen[key] = true
	}
	return nil
}

func (p ArtifactIndexProjection) Validate() error {
	switch p.Role {
	case "title", "text", "trigger":
	default:
		return fmt.Errorf("invalid role %q", p.Role)
	}
	if p.Pointer == "" || p.Pointer[0] != '/' || len(p.Pointer) > 512 {
		return fmt.Errorf("invalid JSON pointer %q", p.Pointer)
	}
	for i := 0; i < len(p.Pointer); i++ {
		if p.Pointer[i] == '~' && (i+1 >= len(p.Pointer) || (p.Pointer[i+1] != '0' && p.Pointer[i+1] != '1')) {
			return fmt.Errorf("invalid JSON pointer escape in %q", p.Pointer)
		}
	}
	return nil
}

func (d LifecycleDef) Validate() error {
	if len(d.Points) > maxLifecyclePoints || len(d.Events) > maxLifecycleEvents {
		return errors.New("too many lifecycle subscriptions")
	}
	if d.Failure != "" && d.Failure != "open" && d.Failure != "closed" {
		return fmt.Errorf("failure must be open or closed, got %q", d.Failure)
	}
	if d.TimeoutMS < 0 || d.TimeoutMS > maximumLifecycleTimeoutMS {
		return fmt.Errorf("timeout_ms outside 0..%d", maximumLifecycleTimeoutMS)
	}
	validPoints := map[string]bool{
		"pre_llm": true, "post_llm": true, "pre_tool": true,
		"post_tool": true, "post_turn": true,
	}
	if err := validateSubscriptions(d.Points, validPoints, "point"); err != nil {
		return err
	}
	for _, event := range d.Events {
		if !validLifecycleName(event) {
			return fmt.Errorf("invalid lifecycle event %q", event)
		}
	}
	if duplicates(d.Events) {
		return errors.New("duplicate lifecycle event")
	}
	return nil
}

func (d LifecycleDef) EffectiveTimeoutMS() int {
	if d.TimeoutMS == 0 {
		return defaultLifecycleTimeoutMS
	}
	return d.TimeoutMS
}

func validateSubscriptions(values []string, allowed map[string]bool, label string) error {
	seen := map[string]bool{}
	for _, value := range values {
		if !allowed[value] {
			return fmt.Errorf("invalid lifecycle %s %q", label, value)
		}
		if seen[value] {
			return fmt.Errorf("duplicate lifecycle %s %q", label, value)
		}
		seen[value] = true
	}
	return nil
}

func validLifecycleName(value string) bool {
	if value == "" || len(value) > 128 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return strings.Contains(value, ".")
}

func duplicates(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] {
			return true
		}
		seen[value] = true
	}
	return false
}

func compileArtifactSchema(name, raw string) (*jsonschema.Schema, error) {
	var root map[string]any
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return nil, fmt.Errorf("schema JSON: %w", err)
	}
	if root["type"] != "object" {
		return nil, errors.New("artifact data schema root type must be object")
	}
	nodes := 0
	if err := validateArtifactSchemaSubset(root, 0, &nodes); err != nil {
		return nil, err
	}
	compiler := jsonschema.NewCompiler()
	url := "https://stado.dev/plugin-artifact/" + name + "/schema.json"
	if err := compiler.AddResource(url, root); err != nil {
		return nil, fmt.Errorf("schema resource: %w", err)
	}
	compiled, err := compiler.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("schema compile: %w", err)
	}
	return compiled, nil
}

// validateArtifactSchemaSubset keeps signed artifact schemas deterministic and
// self-contained. Unknown JSON Schema vocabulary is rejected instead of being
// silently ignored, and references/identifiers are intentionally excluded so a
// schema cannot fetch or reinterpret an external resource during load/rebuild.
func validateArtifactSchemaSubset(schema map[string]any, depth int, nodes *int) error {
	if depth > maxArtifactSchemaDepth {
		return fmt.Errorf("artifact schema exceeds maximum depth %d", maxArtifactSchemaDepth)
	}
	*nodes++
	if *nodes > maxArtifactSchemaNodes {
		return fmt.Errorf("artifact schema exceeds maximum node count %d", maxArtifactSchemaNodes)
	}
	for keyword, value := range schema {
		switch keyword {
		case "$schema":
			if value != "https://json-schema.org/draft/2020-12/schema" {
				return fmt.Errorf("unsupported $schema %q", value)
			}
		case "type":
			if err := validateSchemaType(value); err != nil {
				return err
			}
		case "properties":
			properties, ok := value.(map[string]any)
			if !ok {
				return errors.New("schema properties must be an object")
			}
			for name, child := range properties {
				childSchema, ok := child.(map[string]any)
				if !ok {
					return fmt.Errorf("schema property %q must be a schema object", name)
				}
				if err := validateArtifactSchemaSubset(childSchema, depth+1, nodes); err != nil {
					return fmt.Errorf("property %q: %w", name, err)
				}
			}
		case "items", "contains", "not", "if", "then", "else":
			child, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("schema %s must be an object", keyword)
			}
			if err := validateArtifactSchemaSubset(child, depth+1, nodes); err != nil {
				return fmt.Errorf("%s: %w", keyword, err)
			}
		case "additionalProperties":
			if _, ok := value.(bool); ok {
				continue
			}
			child, ok := value.(map[string]any)
			if !ok {
				return errors.New("schema additionalProperties must be boolean or schema object")
			}
			if err := validateArtifactSchemaSubset(child, depth+1, nodes); err != nil {
				return fmt.Errorf("additionalProperties: %w", err)
			}
		case "allOf", "anyOf", "oneOf", "prefixItems":
			children, ok := value.([]any)
			if !ok || len(children) == 0 || len(children) > 64 {
				return fmt.Errorf("schema %s must contain 1..64 schema objects", keyword)
			}
			for i, rawChild := range children {
				child, ok := rawChild.(map[string]any)
				if !ok {
					return fmt.Errorf("schema %s[%d] must be an object", keyword, i)
				}
				if err := validateArtifactSchemaSubset(child, depth+1, nodes); err != nil {
					return fmt.Errorf("%s[%d]: %w", keyword, i, err)
				}
			}
		case "required":
			if err := validateStringArrayKeyword(keyword, value, 256); err != nil {
				return err
			}
		case "enum":
			values, ok := value.([]any)
			if !ok || len(values) == 0 || len(values) > 256 {
				return errors.New("schema enum must contain 1..256 values")
			}
		case "pattern":
			pattern, ok := value.(string)
			if !ok || len(pattern) > maxArtifactSchemaRegex {
				return fmt.Errorf("schema pattern must be a string of at most %d bytes", maxArtifactSchemaRegex)
			}
			if _, err := regexp.Compile(pattern); err != nil {
				return fmt.Errorf("schema pattern: %w", err)
			}
		case "minLength", "maxLength", "minItems", "maxItems", "minProperties", "maxProperties":
			if !nonNegativeJSONInteger(value) {
				return fmt.Errorf("schema %s must be a non-negative integer", keyword)
			}
		case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf":
			if _, ok := value.(json.Number); !ok {
				return fmt.Errorf("schema %s must be a number", keyword)
			}
		case "uniqueItems":
			if _, ok := value.(bool); !ok {
				return errors.New("schema uniqueItems must be boolean")
			}
		case "const", "title", "description", "default", "examples":
			// Validation-neutral annotations and literal constraints are bounded
			// by the enclosing signed schema byte limit.
		case "$id", "$ref", "$dynamicRef", "$anchor", "$dynamicAnchor",
			"unevaluatedProperties", "unevaluatedItems", "dependentSchemas",
			"patternProperties", "propertyNames", "format", "contentEncoding",
			"contentMediaType", "contentSchema":
			return fmt.Errorf("unsupported artifact schema keyword %q", keyword)
		default:
			return fmt.Errorf("unknown artifact schema keyword %q", keyword)
		}
	}
	return nil
}

func validateSchemaType(value any) error {
	valid := func(name string) bool {
		switch name {
		case "null", "boolean", "object", "array", "number", "integer", "string":
			return true
		}
		return false
	}
	switch typed := value.(type) {
	case string:
		if valid(typed) {
			return nil
		}
	case []any:
		if len(typed) == 0 || len(typed) > 7 {
			break
		}
		seen := map[string]bool{}
		for _, raw := range typed {
			name, ok := raw.(string)
			if !ok || !valid(name) || seen[name] {
				return errors.New("schema type array contains invalid or duplicate type")
			}
			seen[name] = true
		}
		return nil
	}
	return errors.New("schema type must be a supported type or unique type array")
}

func validateStringArrayKeyword(keyword string, value any, limit int) error {
	values, ok := value.([]any)
	if !ok || len(values) > limit {
		return fmt.Errorf("schema %s must be a string array of at most %d entries", keyword, limit)
	}
	seen := map[string]bool{}
	for _, raw := range values {
		item, ok := raw.(string)
		if !ok || item == "" || seen[item] {
			return fmt.Errorf("schema %s contains invalid or duplicate string", keyword)
		}
		seen[item] = true
	}
	return nil
}

func nonNegativeJSONInteger(value any) bool {
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	parsed, err := number.Int64()
	return err == nil && parsed >= 0
}

func (d ArtifactKindDef) SchemaDigest() string {
	sum := sha256.Sum256([]byte(d.Schema))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ValidateData applies the signed kind schema to one artifact data object.
// Callers should retain the exact ArtifactKindDef alongside historical records;
// validating against a newly installed version would reinterpret old data.
func (d ArtifactKindDef) ValidateData(raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("artifact data object required")
	}
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return fmt.Errorf("artifact data JSON: %w", err)
	}
	if _, ok := value.(map[string]any); !ok {
		return errors.New("artifact data must be a JSON object")
	}
	compiled, err := compileArtifactSchema(d.Name, d.Schema)
	if err != nil {
		return err
	}
	if err := compiled.Validate(value); err != nil {
		return fmt.Errorf("artifact data schema: %w", err)
	}
	return nil
}

func (m Manifest) ManifestDigest() (string, error) {
	raw, err := m.Canonical()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
