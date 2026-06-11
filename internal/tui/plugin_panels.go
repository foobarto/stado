package tui

// Plugin-contributed chrome panels (#21 part 2). A plugin emits a
// stado_ui_render panel with Target "sidebar" / "footer" / "log"; the
// TUI stores it (last-write-wins per Panel.ID) and renders it only when
// the operator lists that id in [tui.sidebar].sections / [tui.footer].
// segments. Built-in section/segment ids dispatch to native blocks
// before the plugin lookup, so a plugin can never override them.
//
// Plugins never choose arbitrary colours: Panel.Variant maps to a SAFE
// theme tone via pluginToneForVariant, keeping plugin output consistent
// with the active theme and the sandbox boundary.

import (
	"fmt"
	"strings"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// pluginLogPrefix tags a log-target render line in the shared sidebar
// log tail so it is tone-mapped and surfaced regardless of
// --sidebar-debug (the plugin author chose to emit it). Mirrors the
// "PROGRESS " convention in logtail.go.
const pluginLogPrefix = "PLUGIN "

// maxPluginChromePanels caps how many DISTINCT plugin panel ids each
// chrome store (sidebar / footer) retains. Without a bound, a buggy or
// hostile plugin holding the ui:render cap could emit renders with an
// ever-changing id and grow the store until OOM (the log target is
// already bounded by maxSidebarLogLines). New-id growth past the cap is
// dropped; last-write-wins on an already-stored id stays free, so a
// well-behaved plugin updating a fixed set of panels is never affected.
const maxPluginChromePanels = 32

// pluginSidebarPanel is the flattened, render-ready form of a plugin
// sidebar panel. The sidebar template renders Heading (bold) + Lines
// (tone-coloured, width-wrapped) exactly like a built-in section, so
// wrap/colour stay in one place (the template's FuncMap).
type pluginSidebarPanel struct {
	Heading string
	Lines   []sidebarLine
}

// pluginToneForVariant maps a plugin Panel.Variant to a SAFE theme tone
// name. The variant enum is the only styling lever a plugin has; it
// resolves to a theme-managed tone so plugin output never picks a raw
// colour. info/"" → text, ok → success, warn → warning, error → error,
// recommendation → accent.
func pluginToneForVariant(variant string) string {
	switch variant {
	case "ok":
		return "success"
	case "warn":
		return "warning"
	case "error":
		return "error"
	case "recommendation":
		return "accent"
	default: // "" / "info"
		return "text"
	}
}

// flattenPluginSidebarPanel turns a decoded Panel into a compact sidebar
// block: the Title becomes the bold heading, each section flattens to
// tone-coloured lines, and the Footer (if any) trails as a muted line.
// Rich bodies (code/table/diff) render a one-line placeholder — the
// sidebar is narrow; full rendering stays viewport-only.
func flattenPluginSidebarPanel(panel pluginRuntime.Panel) pluginSidebarPanel {
	tone := pluginToneForVariant(panel.Variant)
	out := pluginSidebarPanel{Heading: panel.Title}
	for _, sec := range panel.Sections {
		if sec.Heading != "" {
			out.Lines = append(out.Lines, sidebarLine{Text: sec.Heading, Tone: "text_secondary"})
		}
		switch sec.Kind {
		case "text":
			for _, ln := range strings.Split(sec.Text, "\n") {
				if strings.TrimSpace(ln) == "" {
					continue
				}
				out.Lines = append(out.Lines, sidebarLine{Text: ln, Tone: tone})
			}
		case "kv":
			for _, p := range sec.KV {
				out.Lines = append(out.Lines, sidebarLine{Text: p.Label + ": " + p.Value, Tone: tone})
			}
		case "list":
			marker := "·"
			if sec.List.Marker == "check" {
				marker = "▪"
			}
			for _, it := range sec.List.Items {
				out.Lines = append(out.Lines, sidebarLine{Text: marker + " " + it, Tone: tone})
			}
		case "code":
			lang := sec.Code.Language
			if lang == "" {
				lang = "code"
			}
			out.Lines = append(out.Lines, sidebarLine{Text: "[" + lang + " block]", Tone: "muted"})
		case "table":
			out.Lines = append(out.Lines, sidebarLine{
				Text: fmt.Sprintf("[table %d×%d]", len(sec.Table.Rows), len(sec.Table.Columns)),
				Tone: "muted",
			})
		case "diff":
			out.Lines = append(out.Lines, sidebarLine{Text: "[diff]", Tone: "muted"})
		}
	}
	if panel.Footer != "" {
		out.Lines = append(out.Lines, sidebarLine{Text: panel.Footer, Tone: "muted"})
	}
	return out
}

// pluginFooterText renders a plugin footer-target panel to a single
// short line for the cramped footer: the Footer if set, else the Title.
// Empty when neither is present (caller skips the segment).
func pluginFooterText(panel pluginRuntime.Panel) string {
	text := strings.TrimSpace(panel.Footer)
	if text == "" {
		text = strings.TrimSpace(panel.Title)
	}
	if text == "" {
		return ""
	}
	return trimSeed(text, 32)
}

// pluginLogLine renders a log-target panel to one notification-log line:
// "PLUGIN <title>: <footer-or-first-text>". Bounded by the log tail cap.
func pluginLogLine(panel pluginRuntime.Panel) string {
	detail := strings.TrimSpace(panel.Footer)
	if detail == "" {
		for _, sec := range panel.Sections {
			if sec.Kind == "text" && strings.TrimSpace(sec.Text) != "" {
				detail = strings.TrimSpace(strings.Split(sec.Text, "\n")[0])
				break
			}
		}
	}
	title := strings.TrimSpace(panel.Title)
	line := pluginLogPrefix + title
	if detail != "" {
		line += ": " + detail
	}
	return trimSeed(line, 120)
}
