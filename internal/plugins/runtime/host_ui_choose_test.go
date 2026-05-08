package runtime

import (
	"strings"
	"testing"
)

func TestDecodeChooseRequest_Valid(t *testing.T) {
	w := chooseRequestWire{
		Prompt: "Pick one",
		Options: []chooseOptionWire{
			{ID: "a", Label: "Alpha"},
			{ID: "b", Label: "Bravo"},
		},
		Multi:   false,
		Default: []string{"a"},
	}
	got, err := decodeChooseRequest(w)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Prompt != "Pick one" || len(got.Options) != 2 || got.Multi {
		t.Fatalf("decode shape: %+v", got)
	}
	if got.Options[0].ID != "a" || got.Options[0].Label != "Alpha" {
		t.Errorf("options not preserved: %+v", got.Options)
	}
}

func TestDecodeChooseRequest_RejectsBadInputs(t *testing.T) {
	cases := []struct {
		name     string
		wire     chooseRequestWire
		wantSubs string
	}{
		{
			name:     "no options",
			wire:     chooseRequestWire{Prompt: "p"},
			wantSubs: "at least one option",
		},
		{
			name: "empty id",
			wire: chooseRequestWire{Prompt: "p", Options: []chooseOptionWire{
				{ID: "", Label: "x"},
			}},
			wantSubs: "id required",
		},
		{
			name: "duplicate id",
			wire: chooseRequestWire{Prompt: "p", Options: []chooseOptionWire{
				{ID: "x", Label: "X1"},
				{ID: "x", Label: "X2"},
			}},
			wantSubs: "duplicate id",
		},
		{
			name: "id too long",
			wire: chooseRequestWire{Prompt: "p", Options: []chooseOptionWire{
				{ID: strings.Repeat("a", maxPluginRuntimeUIChooseIDBytes+1), Label: "x"},
			}},
			wantSubs: "exceeds",
		},
		{
			name: "label too long",
			wire: chooseRequestWire{Prompt: "p", Options: []chooseOptionWire{
				{ID: "x", Label: strings.Repeat("L", maxPluginRuntimeUIChooseLabelBytes+1)},
			}},
			wantSubs: "label exceeds",
		},
		{
			name: "prompt too long",
			wire: chooseRequestWire{
				Prompt: strings.Repeat("p", maxPluginRuntimeUIChoosePromptBytes+1),
				Options: []chooseOptionWire{
					{ID: "x", Label: "X"},
				},
			},
			wantSubs: "prompt exceeds",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := decodeChooseRequest(tc.wire)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSubs) {
				t.Errorf("err = %q, want contains %q", err.Error(), tc.wantSubs)
			}
		})
	}
}

// TestDecodeChooseRequest_F10Fields: the new prefix / input /
// validator wire fields decode cleanly when present, leave the
// runtime ChoiceOption with nil Input when absent (pre-F10 shape),
// and reject malformed validator declarations at decode time so
// the choice modal never opens with a bad spec.
func TestDecodeChooseRequest_F10Fields(t *testing.T) {
	t.Run("input + validator decode end-to-end", func(t *testing.T) {
		w := chooseRequestWire{
			Prompt: "Pick + parameterise",
			Options: []chooseOptionWire{
				{
					ID:     "run",
					Label:  "Run with model",
					Prefix: "model:",
					Input: &chooseInputWire{
						Default: "gpt-5.5",
						Validator: &chooseValidatorWire{
							Kind: "length",
							Spec: "1,40",
						},
					},
				},
				{ID: "skip", Label: "Skip"},
			},
		}
		got, err := decodeChooseRequest(w)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Options[0].Prefix != "model:" {
			t.Errorf("prefix not preserved: %+v", got.Options[0])
		}
		in := got.Options[0].Input
		if in == nil {
			t.Fatal("input should be non-nil")
		}
		if in.Default != "gpt-5.5" {
			t.Errorf("default not preserved: %q", in.Default)
		}
		if in.Validator == nil || in.Validator.Kind != "length" || in.Validator.Spec != "1,40" {
			t.Errorf("validator not preserved: %+v", in.Validator)
		}
		if got.Options[1].Input != nil {
			t.Errorf("option without input should keep Input nil; got %+v", got.Options[1].Input)
		}
	})

	t.Run("rejects unknown validator kind", func(t *testing.T) {
		w := chooseRequestWire{Prompt: "p", Options: []chooseOptionWire{{
			ID: "x", Label: "X",
			Input: &chooseInputWire{Validator: &chooseValidatorWire{Kind: "luhn"}},
		}}}
		_, err := decodeChooseRequest(w)
		if err == nil {
			t.Fatal("expected error for unknown validator kind")
		}
		if !strings.Contains(err.Error(), "not supported") {
			t.Errorf("err = %q, want contains 'not supported'", err.Error())
		}
	})

	t.Run("rejects malformed regex spec", func(t *testing.T) {
		w := chooseRequestWire{Prompt: "p", Options: []chooseOptionWire{{
			ID: "x", Label: "X",
			Input: &chooseInputWire{Validator: &chooseValidatorWire{Kind: "regex", Spec: "[unterminated"}},
		}}}
		_, err := decodeChooseRequest(w)
		if err == nil {
			t.Fatal("expected error for invalid regex")
		}
		if !strings.Contains(err.Error(), "regex") {
			t.Errorf("err = %q, want contains 'regex'", err.Error())
		}
	})

	t.Run("rejects oversize prefix", func(t *testing.T) {
		w := chooseRequestWire{Prompt: "p", Options: []chooseOptionWire{{
			ID: "x", Label: "X",
			Prefix: strings.Repeat("p", maxPluginRuntimeUIChoosePrefixBytes+1),
		}}}
		_, err := decodeChooseRequest(w)
		if err == nil {
			t.Fatal("expected error for oversize prefix")
		}
		if !strings.Contains(err.Error(), "prefix") {
			t.Errorf("err = %q, want contains 'prefix'", err.Error())
		}
	})

	t.Run("rejects oversize input default", func(t *testing.T) {
		w := chooseRequestWire{Prompt: "p", Options: []chooseOptionWire{{
			ID: "x", Label: "X",
			Input: &chooseInputWire{
				Default: strings.Repeat("d", maxPluginRuntimeUIChooseInputDefaultBytes+1),
			},
		}}}
		_, err := decodeChooseRequest(w)
		if err == nil {
			t.Fatal("expected error for oversize default")
		}
		if !strings.Contains(err.Error(), "default") {
			t.Errorf("err = %q, want contains 'default'", err.Error())
		}
	})
}

// TestDecodeChooseRequest_PreF10ShapeUnchanged: the original
// {id, label} options-without-prefix-or-input shape decodes
// identically — no Input populated, Prefix empty. Wire compat
// gate.
func TestDecodeChooseRequest_PreF10ShapeUnchanged(t *testing.T) {
	w := chooseRequestWire{
		Prompt: "Pick",
		Options: []chooseOptionWire{
			{ID: "a", Label: "Alpha"},
			{ID: "b", Label: "Bravo"},
		},
	}
	got, err := decodeChooseRequest(w)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for i, o := range got.Options {
		if o.Prefix != "" || o.Input != nil {
			t.Errorf("option %d should have zero F10 fields, got prefix=%q input=%+v",
				i, o.Prefix, o.Input)
		}
	}
}

func TestDecodeChooseRequest_TooManyOptions(t *testing.T) {
	wire := chooseRequestWire{Prompt: "p"}
	for i := 0; i <= maxPluginRuntimeUIChooseOptions; i++ {
		wire.Options = append(wire.Options, chooseOptionWire{
			ID: "id" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Label: "L",
		})
	}
	_, err := decodeChooseRequest(wire)
	if err == nil {
		t.Fatal("expected error for too many options")
	}
	if !strings.Contains(err.Error(), "too many options") {
		t.Errorf("err = %q, want contains 'too many options'", err.Error())
	}
}
