package runtime

import (
	"strings"
	"testing"
)

// TestDecodePrintRequest_Valid: a well-formed print request decodes
// into the runtime's PrintOpts shape with EOL defaulting to true
// when absent. F9a.
func TestDecodePrintRequest_Valid(t *testing.T) {
	w := printRequestWire{
		Text:     "hello",
		Severity: "warn",
		StreamID: "scan-progress",
	}
	text, opts, err := decodePrintRequest(w)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if text != "hello" {
		t.Errorf("text = %q, want %q", text, "hello")
	}
	if opts.Severity != "warn" {
		t.Errorf("severity = %q, want %q", opts.Severity, "warn")
	}
	if opts.StreamID != "scan-progress" {
		t.Errorf("stream_id = %q, want %q", opts.StreamID, "scan-progress")
	}
	if !opts.EOL {
		t.Error("EOL should default to true when absent")
	}
}

// TestDecodePrintRequest_ExplicitEOLFalse: when the wire payload
// passes "eol": false explicitly, the decoded opts must reflect it
// — distinguishable from the absence-default. F9a.
func TestDecodePrintRequest_ExplicitEOLFalse(t *testing.T) {
	noEOL := false
	w := printRequestWire{Text: "x", EOL: &noEOL}
	_, opts, err := decodePrintRequest(w)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if opts.EOL {
		t.Error("explicit eol=false should override the default")
	}
}

// TestDecodePrintRequest_RejectsBadInputs: shape violations surface
// as decode-time errors with operator-readable substrings so the
// plugin can map them. F9a.
func TestDecodePrintRequest_RejectsBadInputs(t *testing.T) {
	cases := []struct {
		name, want string
		wire       printRequestWire
	}{
		{
			name: "text too long",
			wire: printRequestWire{Text: strings.Repeat("x", int(maxPluginRuntimeUIPrintTextBytes)+1)},
			want: "text exceeds",
		},
		{
			name: "unknown severity",
			wire: printRequestWire{Text: "x", Severity: "fatal"},
			want: "severity",
		},
		{
			name: "stream_id too long",
			wire: printRequestWire{Text: "x", StreamID: strings.Repeat("s", maxPluginRuntimeUIPrintStreamIDBytes+1)},
			want: "stream_id exceeds",
		},
		{
			name: "empty text and stream_id",
			wire: printRequestWire{},
			want: "text required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := decodePrintRequest(tc.wire)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want contains %q", err.Error(), tc.want)
			}
		})
	}
}

// TestDecodePrintRequest_AcceptsAllValidSeverities: each documented
// severity (including the empty default) is accepted. F9a.
func TestDecodePrintRequest_AcceptsAllValidSeverities(t *testing.T) {
	for _, s := range []string{"", "info", "warn", "error"} {
		t.Run(s, func(t *testing.T) {
			_, _, err := decodePrintRequest(printRequestWire{Text: "x", Severity: s})
			if err != nil {
				t.Errorf("severity %q rejected: %v", s, err)
			}
		})
	}
}
