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
