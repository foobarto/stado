package runtime

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Choice validator kind names. Stable across releases — adding new
// kinds is additive; renames or removals would be a breaking change
// to the stado_ui_choose wire surface.
const (
	ChoiceValidatorKindLength    = "length"
	ChoiceValidatorKindRegex     = "regex"
	ChoiceValidatorKindInt       = "int"
	ChoiceValidatorKindPath      = "path"
	ChoiceValidatorKindMultiline = "multiline"
)

// validateChoiceValidatorShape checks the validator declaration
// itself (kind in the supported set, spec parses for the kinds that
// require one). Runs at decode time so a malformed validator gets
// rejected before the choice modal ever opens. F10.
func validateChoiceValidatorShape(kind, spec string) error {
	switch kind {
	case ChoiceValidatorKindLength:
		if _, _, err := parseLengthSpec(spec); err != nil {
			return fmt.Errorf("validator length: %w", err)
		}
		return nil
	case ChoiceValidatorKindRegex:
		if spec == "" {
			return errors.New("validator regex: spec required")
		}
		if _, err := regexp.Compile(spec); err != nil {
			return fmt.Errorf("validator regex: %w", err)
		}
		return nil
	case ChoiceValidatorKindInt, ChoiceValidatorKindPath, ChoiceValidatorKindMultiline:
		return nil
	case "":
		return errors.New("validator kind required")
	default:
		return fmt.Errorf("validator kind %q not supported", kind)
	}
}

// ValidateChoiceInput runs the configured validator against the
// operator's typed value. Returns nil when the input is acceptable
// or when validator is nil. The error message is operator-facing —
// surfaced inline in the choice drawer when validation fails. F10.
func ValidateChoiceInput(input string, v *ChoiceValidator) error {
	if v == nil {
		return nil
	}
	switch v.Kind {
	case ChoiceValidatorKindLength:
		minN, maxN, err := parseLengthSpec(v.Spec)
		if err != nil {
			return err
		}
		n := len(input)
		if n < minN {
			return fmt.Errorf("input must be at least %d characters", minN)
		}
		if maxN > 0 && n > maxN {
			return fmt.Errorf("input must be at most %d characters", maxN)
		}
		return nil
	case ChoiceValidatorKindRegex:
		re, err := regexp.Compile(v.Spec)
		if err != nil {
			return fmt.Errorf("validator regex: %w", err)
		}
		if !re.MatchString(input) {
			return fmt.Errorf("input does not match required pattern")
		}
		return nil
	case ChoiceValidatorKindInt:
		if _, err := strconv.Atoi(strings.TrimSpace(input)); err != nil {
			return fmt.Errorf("input must be an integer")
		}
		return nil
	case ChoiceValidatorKindPath:
		if input == "" || !filepath.IsLocal(input) {
			return fmt.Errorf("input must be a local filesystem path")
		}
		return nil
	case ChoiceValidatorKindMultiline:
		// Presence-only flag — affects rendering, not validity.
		return nil
	default:
		return fmt.Errorf("validator kind %q not supported", v.Kind)
	}
}

// parseLengthSpec parses the "min,max" length validator spec.
// Either side may be omitted (e.g. "0,80", "5,", ",120"); zero
// max means "no upper bound." Returns (min, max, err).
func parseLengthSpec(spec string) (int, int, error) {
	if spec == "" {
		return 0, 0, errors.New("spec required (use \"min,max\")")
	}
	parts := strings.SplitN(spec, ",", 2)
	if len(parts) != 2 {
		return 0, 0, errors.New("spec must be \"min,max\"")
	}
	minStr := strings.TrimSpace(parts[0])
	maxStr := strings.TrimSpace(parts[1])
	minN := 0
	maxN := 0
	if minStr != "" {
		v, err := strconv.Atoi(minStr)
		if err != nil || v < 0 {
			return 0, 0, fmt.Errorf("min must be a non-negative integer, got %q", minStr)
		}
		minN = v
	}
	if maxStr != "" {
		v, err := strconv.Atoi(maxStr)
		if err != nil || v < 0 {
			return 0, 0, fmt.Errorf("max must be a non-negative integer, got %q", maxStr)
		}
		maxN = v
	}
	if maxN > 0 && minN > maxN {
		return 0, 0, fmt.Errorf("min %d > max %d", minN, maxN)
	}
	return minN, maxN, nil
}
