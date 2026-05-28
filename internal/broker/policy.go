package broker

import (
	"errors"
	"fmt"
)

// ErrPolicyNotLoaded is returned by Evaluate when the broker
// service has no policy file loaded (initial state or load failure).
var ErrPolicyNotLoaded = errors.New("broker: policy not loaded")

// Policy is the broker's loaded capability policy. Phase 1 ships
// the permissive default; phase 1b fills in the TOML loader and
// real evaluation semantics. The interface is stable across phases.
type Policy struct {
	// DefaultAdmit is the global fallback when no more-specific
	// rule fires. Permissive in the phase-1 default policy.
	DefaultAdmit bool

	// PurposeAdmits maps Purpose → admit/deny. Missing key falls
	// back to DefaultAdmit.
	PurposeAdmits map[Purpose]bool

	// ProfileAdmits maps Profile → admit/deny. Missing key falls
	// back to DefaultAdmit.
	ProfileAdmits map[Profile]bool

	// PluginAdmits maps plugin name → admit/deny for PurposeToolRun
	// requests. Missing key falls back to DefaultAdmit.
	PluginAdmits map[string]bool
}

// DefaultPolicy returns the policy shipped in the binary when no
// operator-provided policy.toml is present (or as a fallback when
// the operator's policy fails to load — phase 1b decides whether to
// fall back or refuse). All admissions default to true so phase 1
// is a behavioural no-op for existing users.
func DefaultPolicy() *Policy {
	return &Policy{
		DefaultAdmit:  true,
		PurposeAdmits: map[Purpose]bool{},
		ProfileAdmits: map[Profile]bool{},
		PluginAdmits:  map[string]bool{},
	}
}

// Evaluate evaluates req against p and returns a Decision using
// two-pass resolution: pass 1 returns the first explicit DENY at
// any level (plugin override for tool-run → purpose → profile),
// pass 2 returns the first explicit ALLOW at the same levels,
// DefaultAdmit is the fallback. Decision.Rule names which rule
// fired. Explicit DENY anywhere wins over ALLOW elsewhere — see
// the Codex P1 rationale in the inline comment below the body.
//
// Phase 1: the default policy admits everything, so this function
// returns Admit=true with Rule from the purpose dimension. Phase 1b's
// TOML loader populates the per-purpose / per-profile / per-plugin
// maps and tightens the resolution; phase 1c adds the
// broker.v1.policy.query dispatch path that exposes Evaluate over
// the IPC.
func (p *Policy) Evaluate(req CapabilityRequest) Decision {
	if p == nil {
		return Decision{Admit: false, Rule: "no-policy", Reason: ErrPolicyNotLoaded.Error()}
	}

	// Two-pass evaluation: explicit DENY anywhere wins over ALLOW
	// elsewhere. This means an operator who sets `[profile]
	// "no-sandbox" = false` can deny that profile even when the
	// shipped default has `[purpose] main-chat = true` — the profile
	// deny is consulted before purpose admits. Codex P1 review of
	// PR #71.
	//
	// Order within each pass mirrors specificity: plugin (most
	// specific) → purpose → profile (least specific in the
	// per-request dimension). First match wins within each pass.

	// Pass 1: explicit denies.
	if req.Purpose == PurposeToolRun && req.PluginName != "" {
		if admit, ok := p.PluginAdmits[req.PluginName]; ok && !admit {
			return Decision{
				Admit:  false,
				Rule:   fmt.Sprintf("plugin:%s", req.PluginName),
				Reason: decisionReason(false, "plugin"),
			}
		}
	}
	if admit, ok := p.PurposeAdmits[req.Purpose]; ok && !admit {
		return Decision{
			Admit:  false,
			Rule:   fmt.Sprintf("purpose:%s", req.Purpose),
			Reason: decisionReason(false, "purpose"),
		}
	}
	if admit, ok := p.ProfileAdmits[req.Profile]; ok && !admit {
		return Decision{
			Admit:  false,
			Rule:   fmt.Sprintf("profile:%s", req.Profile),
			Reason: decisionReason(false, "profile"),
		}
	}

	// Pass 2: explicit allows (no deny fired above).
	if req.Purpose == PurposeToolRun && req.PluginName != "" {
		if admit, ok := p.PluginAdmits[req.PluginName]; ok && admit {
			return Decision{
				Admit:  true,
				Rule:   fmt.Sprintf("plugin:%s", req.PluginName),
				Reason: "",
			}
		}
	}
	if admit, ok := p.PurposeAdmits[req.Purpose]; ok && admit {
		return Decision{
			Admit:  true,
			Rule:   fmt.Sprintf("purpose:%s", req.Purpose),
			Reason: "",
		}
	}
	if admit, ok := p.ProfileAdmits[req.Profile]; ok && admit {
		return Decision{
			Admit:  true,
			Rule:   fmt.Sprintf("profile:%s", req.Profile),
			Reason: "",
		}
	}

	return Decision{
		Admit:  p.DefaultAdmit,
		Rule:   "default",
		Reason: decisionReason(p.DefaultAdmit, "default"),
	}
}

func decisionReason(admit bool, ruleKind string) string {
	if admit {
		return ""
	}
	return "denied by " + ruleKind + " rule"
}
