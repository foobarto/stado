package runtime

import (
	"strings"
	"testing"
)

// TestValidateChoiceValidatorShape_AcceptsKnownKinds: the
// shape-validation gate at decode time accepts each documented
// validator kind with a well-formed spec.
func TestValidateChoiceValidatorShape_AcceptsKnownKinds(t *testing.T) {
	cases := []struct {
		kind, spec string
	}{
		{ChoiceValidatorKindLength, "0,80"},
		{ChoiceValidatorKindLength, "5,"},
		{ChoiceValidatorKindLength, ",120"},
		{ChoiceValidatorKindRegex, "^[a-z]+$"},
		{ChoiceValidatorKindInt, ""},
		{ChoiceValidatorKindPath, ""},
		{ChoiceValidatorKindMultiline, ""},
	}
	for _, tc := range cases {
		t.Run(tc.kind+":"+tc.spec, func(t *testing.T) {
			if err := validateChoiceValidatorShape(tc.kind, tc.spec); err != nil {
				t.Errorf("kind=%q spec=%q: unexpected err: %v", tc.kind, tc.spec, err)
			}
		})
	}
}

// TestValidateChoiceValidatorShape_RejectsUnknownAndMalformed: the
// gate rejects unknown kinds and malformed specs with operator-
// readable errors. Substring assertions guard the message shape so
// a refactor doesn't silently strip the diagnostic.
func TestValidateChoiceValidatorShape_RejectsUnknownAndMalformed(t *testing.T) {
	cases := []struct {
		name, kind, spec, want string
	}{
		{"empty kind", "", "", "kind required"},
		{"unknown kind", "luhn", "", "not supported"},
		{"length missing spec", "length", "", "spec required"},
		{"length single field", "length", "10", "min,max"},
		{"length non-int min", "length", "x,10", "non-negative integer"},
		{"length min greater than max", "length", "20,5", "min 20 > max 5"},
		{"regex empty spec", "regex", "", "spec required"},
		{"regex bad pattern", "regex", "[unterminated", "validator regex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateChoiceValidatorShape(tc.kind, tc.spec)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want contains %q", err.Error(), tc.want)
			}
		})
	}
}

// TestValidateChoiceInput_LengthBounds: length validator enforces
// min and max, both inclusive; zero max means unbounded.
func TestValidateChoiceInput_LengthBounds(t *testing.T) {
	v := &ChoiceValidator{Kind: "length", Spec: "3,5"}
	cases := []struct {
		input string
		valid bool
	}{
		{"", false},   // below min
		{"ab", false}, // below min
		{"abc", true},
		{"abcd", true},
		{"abcde", true},
		{"abcdef", false}, // above max
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			err := ValidateChoiceInput(tc.input, v)
			if tc.valid && err != nil {
				t.Errorf("valid input %q: %v", tc.input, err)
			}
			if !tc.valid && err == nil {
				t.Errorf("invalid input %q: expected error", tc.input)
			}
		})
	}
}

// TestValidateChoiceInput_LengthZeroMaxIsUnbounded: max=0 means no
// upper bound (used when a caller wants "at least N chars").
func TestValidateChoiceInput_LengthZeroMaxIsUnbounded(t *testing.T) {
	v := &ChoiceValidator{Kind: "length", Spec: "5,"}
	if err := ValidateChoiceInput(strings.Repeat("a", 5000), v); err != nil {
		t.Errorf("max-omitted should be unbounded, got: %v", err)
	}
	if err := ValidateChoiceInput("abc", v); err == nil {
		t.Errorf("min should still apply when max omitted")
	}
}

// TestValidateChoiceInput_Int: int validator accepts integers
// (with whitespace tolerated) and rejects non-numeric / floats.
func TestValidateChoiceInput_Int(t *testing.T) {
	v := &ChoiceValidator{Kind: "int"}
	cases := []struct {
		input string
		valid bool
	}{
		{"0", true},
		{"42", true},
		{"-7", true},
		{" 10 ", true}, // trim whitespace
		{"", false},
		{"abc", false},
		{"1.5", false},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			err := ValidateChoiceInput(tc.input, v)
			if tc.valid && err != nil {
				t.Errorf("valid %q: %v", tc.input, err)
			}
			if !tc.valid && err == nil {
				t.Errorf("invalid %q: expected error", tc.input)
			}
		})
	}
}

// TestValidateChoiceInput_Regex: regex validator compiles the spec
// at validation time and matches the input against it.
func TestValidateChoiceInput_Regex(t *testing.T) {
	v := &ChoiceValidator{Kind: "regex", Spec: "^[A-Z][a-z]+$"}
	if err := ValidateChoiceInput("Alpha", v); err != nil {
		t.Errorf("matching input: %v", err)
	}
	if err := ValidateChoiceInput("alpha", v); err == nil {
		t.Errorf("non-matching input: expected error")
	}
}

// TestValidateChoiceInput_Path: path validator accepts only local
// filesystem paths (no traversal escapes, no absolute paths).
func TestValidateChoiceInput_Path(t *testing.T) {
	v := &ChoiceValidator{Kind: "path"}
	cases := []struct {
		input string
		valid bool
	}{
		{"foo.txt", true},
		{"sub/dir/file", true},
		{"", false},
		{"../escape", false},
		{"/abs/path", false},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			err := ValidateChoiceInput(tc.input, v)
			if tc.valid && err != nil {
				t.Errorf("valid %q: %v", tc.input, err)
			}
			if !tc.valid && err == nil {
				t.Errorf("invalid %q: expected error", tc.input)
			}
		})
	}
}

// TestValidateChoiceInput_NilValidatorAllowsAnything: nil validator
// is the "no checks" case and any input is acceptable.
func TestValidateChoiceInput_NilValidatorAllowsAnything(t *testing.T) {
	if err := ValidateChoiceInput("anything\nat\nall", nil); err != nil {
		t.Errorf("nil validator should accept any input, got: %v", err)
	}
}

// TestValidateChoiceInput_MultilineIsPresenceOnly: multiline kind is
// a rendering hint, not a validation rule — any input passes.
func TestValidateChoiceInput_MultilineIsPresenceOnly(t *testing.T) {
	v := &ChoiceValidator{Kind: "multiline"}
	if err := ValidateChoiceInput("line1\nline2", v); err != nil {
		t.Errorf("multiline should accept any input, got: %v", err)
	}
}
