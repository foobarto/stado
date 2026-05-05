package runtime

import (
	"testing"
)

func TestFormatHandleID_OwnedByPlugin(t *testing.T) {
	got := FormatHandleID(HandleTypeProc, "fs", 0x7a2b)
	want := "proc:fs.7a2b"
	if got != want {
		t.Errorf("FormatHandleID(proc, fs, 0x7a2b) = %q, want %q", got, want)
	}
}

func TestFormatHandleID_LongerHexNotPadded(t *testing.T) {
	got := FormatHandleID(HandleTypeTerminal, "shell", 0x9c1d)
	want := "term:shell.9c1d"
	if got != want {
		t.Errorf("FormatHandleID(term, shell, 0x9c1d) = %q, want %q", got, want)
	}
}

func TestFormatHandleID_FullUint32(t *testing.T) {
	got := FormatHandleID(HandleTypeProc, "x", 0xdeadbeef)
	want := "proc:x.deadbeef"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatHandleID_EmptyPlugin(t *testing.T) {
	// Empty plugin → omit the dotted owner; result is "<type>:<hex>".
	got := FormatHandleID(HandleTypeProc, "", 0x42)
	want := "proc:42"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatFreeStandingHandleID(t *testing.T) {
	got := FormatFreeStandingHandleID(HandleTypeAgent, "bf3eabcdef")
	want := "agent:bf3eabcd" // trimmed to 8 chars
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatFreeStandingHandleID_ShortID(t *testing.T) {
	got := FormatFreeStandingHandleID(HandleTypeSession, "abc")
	want := "session:abc"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseHandleID_OwnedByPlugin(t *testing.T) {
	typ, plugin, h, err := ParseHandleID("proc:fs.7a2b")
	if err != nil {
		t.Fatalf("ParseHandleID error: %v", err)
	}
	if typ != HandleTypeProc {
		t.Errorf("type = %q, want %q", typ, HandleTypeProc)
	}
	if plugin != "fs" {
		t.Errorf("plugin = %q, want %q", plugin, "fs")
	}
	if h != 0x7a2b {
		t.Errorf("h = %#x, want %#x", h, 0x7a2b)
	}
}

func TestParseHandleID_FreeStanding(t *testing.T) {
	typ, plugin, h, err := ParseHandleID("agent:bf3e")
	if err != nil {
		t.Fatalf("ParseHandleID error: %v", err)
	}
	if typ != HandleTypeAgent {
		t.Errorf("type = %q, want %q", typ, HandleTypeAgent)
	}
	if plugin != "" {
		t.Errorf("plugin should be empty for free-standing; got %q", plugin)
	}
	if h != 0 {
		t.Errorf("h should be 0 for free-standing; got %#x", h)
	}
}

func TestParseHandleID_BareNumericRejected(t *testing.T) {
	if _, _, _, err := ParseHandleID("123456"); err == nil {
		t.Error("ParseHandleID(\"123456\") should fail — needs a type prefix")
	}
}

func TestParseHandleID_UnknownType(t *testing.T) {
	if _, _, _, err := ParseHandleID("nope:fs.1"); err == nil {
		t.Error("unknown type prefix should fail")
	}
}

func TestParseHandleID_RoundTrip(t *testing.T) {
	cases := []struct {
		typ    HandleType
		plugin string
		h      uint32
	}{
		{HandleTypeProc, "fs", 0x7a2b},
		{HandleTypeTerminal, "shell", 0x9c1d},
		{HandleTypeProc, "", 0x42},
	}
	for _, c := range cases {
		s := FormatHandleID(c.typ, c.plugin, c.h)
		typ, plugin, h, err := ParseHandleID(s)
		if err != nil {
			t.Errorf("round-trip %q: parse failed: %v", s, err)
			continue
		}
		if typ != c.typ || plugin != c.plugin || h != c.h {
			t.Errorf("round-trip %q: got (%q,%q,%#x), want (%q,%q,%#x)",
				s, typ, plugin, h, c.typ, c.plugin, c.h)
		}
	}
}
