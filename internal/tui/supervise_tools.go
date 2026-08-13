package tui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/foobarto/stado/internal/supervise"
	"github.com/foobarto/stado/internal/trajectory"
	"github.com/foobarto/stado/pkg/tool"
)

const (
	superviseProgressTool   = "supervise__report_progress"
	supervisePivotTool      = "supervise__request_pivot"
	superviseCompletionTool = "supervise__request_completion"
)

func isSuperviseControlTool(name string) bool {
	return name == superviseProgressTool || name == supervisePivotTool || name == superviseCompletionTool
}

type superviseControlTool struct {
	name    string
	runtime *superviseRuntime
}

func (t *superviseControlTool) Name() string      { return t.name }
func (t *superviseControlTool) Class() tool.Class { return tool.ClassStateMutating }
func (t *superviseControlTool) Description() string {
	switch t.name {
	case superviseProgressTool:
		return "Record bounded evidence or a step-completion claim against the active supervised-work plan. This does not approve the claim."
	case supervisePivotTool:
		return "Request approval before changing the supervised-work plan or contract. Tactical implementation adjustments do not need this tool."
	default:
		return "Request independent final verification of supervised work with criterion-linked evidence. This does not mark work complete."
	}
}
func (t *superviseControlTool) Schema() map[string]any {
	stringArray := map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 32}
	evidence := map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"type": "string"}, "summary": map[string]any{"type": "string"}, "references": stringArray}, "required": []string{"kind", "summary"}, "additionalProperties": false}
	switch t.name {
	case superviseProgressTool:
		return map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"type": "string"}, "summary": map[string]any{"type": "string"}, "references": stringArray, "step_complete": map[string]any{"type": "boolean"}}, "required": []string{"kind", "summary"}, "additionalProperties": false}
	case supervisePivotTool:
		step := map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "done_when": map[string]any{"type": "string"}}, "required": []string{"id", "title", "done_when"}, "additionalProperties": false}
		baseline := map[string]any{"type": "object", "properties": map[string]any{"objective": map[string]any{"type": "string"}, "constraints": stringArray, "non_goals": stringArray, "acceptance_criteria": stringArray, "plan": map[string]any{"type": "array", "items": step}, "definition_of_done": stringArray, "verification": stringArray, "risks": stringArray}, "required": []string{"objective", "acceptance_criteria", "plan", "definition_of_done", "verification"}, "additionalProperties": false}
		return map[string]any{"type": "object", "properties": map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"plan", "contract"}}, "reason": map[string]any{"type": "string"}, "proposed_plan": map[string]any{"type": "array", "items": step}, "proposed_baseline": baseline}, "required": []string{"kind", "reason"}, "additionalProperties": false}
	default:
		return map[string]any{"type": "object", "properties": map[string]any{"summary": map[string]any{"type": "string"}, "evidence": map[string]any{"type": "array", "items": evidence, "minItems": 1, "maxItems": 128}}, "required": []string{"summary", "evidence"}, "additionalProperties": false}
	}
}

func (t *superviseControlTool) Run(ctx context.Context, args json.RawMessage, _ tool.Host) (tool.Result, error) {
	if t.runtime == nil || t.runtime.service == nil {
		return tool.Result{Error: "supervision is not active"}, errors.New("supervision is not active")
	}
	st, err := t.runtime.service.State(t.runtime.state.ID)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	idem := "supervise-tool:" + t.name + ":" + strconvVersion(st.Version) + ":" + digestBytes(args)
	principal := trajectory.LocalPrincipal()
	switch t.name {
	case superviseProgressTool:
		var in struct {
			Kind         string   `json:"kind"`
			Summary      string   `json:"summary"`
			References   []string `json:"references"`
			StepComplete bool     `json:"step_complete"`
		}
		if err := strictJSON(args, &in); err != nil {
			return tool.Result{Error: err.Error()}, err
		}
		kind := strings.TrimSpace(in.Kind)
		if in.StepComplete {
			kind = "step_completion_claim:" + st.ActiveStep
		}
		st, err = t.runtime.service.RecordEvidence(ctx, st.ID, st.Version, supervise.RoleWorker, supervise.Evidence{Kind: kind, Summary: in.Summary, References: in.References, Anchor: st.Anchor()}, principal, "worker", idem)
	case supervisePivotTool:
		var in struct {
			Kind             supervise.PivotKind `json:"kind"`
			Reason           string              `json:"reason"`
			ProposedPlan     []supervise.Step    `json:"proposed_plan"`
			ProposedBaseline *supervise.Baseline `json:"proposed_baseline"`
		}
		if err := strictJSON(args, &in); err != nil {
			return tool.Result{Error: err.Error()}, err
		}
		st, err = t.runtime.service.RequestPivot(ctx, st.ID, st.Version, supervise.RoleWorker, supervise.PivotRequest{Kind: in.Kind, Reason: in.Reason, ProposedPlan: in.ProposedPlan, ProposedBaseline: in.ProposedBaseline, Anchor: st.Anchor()}, principal, "worker", idem)
	case superviseCompletionTool:
		var in struct {
			Summary  string `json:"summary"`
			Evidence []struct {
				Kind       string   `json:"kind"`
				Summary    string   `json:"summary"`
				References []string `json:"references"`
			} `json:"evidence"`
		}
		if err := strictJSON(args, &in); err != nil {
			return tool.Result{Error: err.Error()}, err
		}
		evidence := make([]supervise.Evidence, 0, len(in.Evidence))
		for _, e := range in.Evidence {
			evidence = append(evidence, supervise.Evidence{Kind: e.Kind, Summary: e.Summary, References: e.References, Anchor: st.Anchor()})
		}
		st, err = t.runtime.service.RequestCompletion(ctx, st.ID, st.Version, supervise.RoleWorker, supervise.CompletionRequest{Summary: in.Summary, Evidence: evidence, Anchor: st.Anchor()}, principal, "worker", idem)
	default:
		err = fmt.Errorf("unknown supervision control tool %q", t.name)
	}
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	raw, _ := json.Marshal(map[string]any{"run_id": st.ID, "status": st.Status, "version": st.Version, "plan_version": st.PlanVersion, "active_step": st.ActiveStep})
	return tool.Result{Content: string(raw)}, nil
}

func (m *Model) registerSuperviseControlTools() {
	if m.executor == nil || m.executor.Registry == nil || m.supervision == nil {
		return
	}
	for _, name := range []string{superviseProgressTool, supervisePivotTool, superviseCompletionTool} {
		m.executor.Registry.Register(&superviseControlTool{name: name, runtime: m.supervision})
	}
}

func (m *Model) syncSuperviseState() {
	if m.supervision == nil || m.supervision.service == nil {
		return
	}
	if st, err := m.supervision.service.State(m.supervision.state.ID); err == nil {
		m.supervision.state = st
	}
}

func strictJSON(raw []byte, out any) error {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}
func digestBytes(raw []byte) string  { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:8]) }
func strconvVersion(v uint64) string { return fmt.Sprintf("%d", v) }
