package tui

import (
	"strings"
	"testing"
)

// TestSystemBlock_LongBodyNotTruncated: regression test for the
// blocks_render.go truncation bug that capped system block bodies at
// `width*6` chars (~480 at 80cols). That cap silently dropped the
// tail of long bodies: `/tool ls` rendered as ~12 of ~180 lines, and
// `/tool fs.read` on any non-trivial file rendered an empty-looking
// result because the "plugin .../read → <content>" envelope was cut
// off after the first ~400 chars of content. Fix removed the cap;
// this test guards against re-introduction.
func TestSystemBlock_LongBodyNotTruncated(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	// Build a body that exceeds the prior width*6 cap at any
	// reasonable terminal width — 10kB of distinct lines so a
	// truncation would be obvious.
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString("line-")
		sb.WriteString(itoa(i))
		sb.WriteString("-payload-payload-payload-payload\n")
	}
	body := sb.String()
	m.appendBlock(block{kind: "system", body: body})

	// renderBlock is the load-bearing call: it's where the cap used to
	// live. Pre-fix this returned a body cut to ~width*6 chars; post-
	// fix it returns the full body wrapped by lipgloss to `width`.
	out, err := m.renderBlock(m.blocks[len(m.blocks)-1], 80)
	if err != nil {
		t.Fatalf("renderBlock: %v", err)
	}
	// Every line marker must survive into the rendered output (lipgloss
	// may re-wrap, but the substring of each marker is preserved).
	for i := 0; i < 200; i++ {
		marker := "line-" + itoa(i)
		if !strings.Contains(out, marker) {
			t.Fatalf("rendered system block dropped %q — truncate cap regressed (out len=%d)", marker, len(out))
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
