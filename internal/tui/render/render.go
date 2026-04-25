// Package render drives template-based rendering for the TUI.
//
// Each visual element (message, tool call, sidebar, status bar) has a .tmpl
// file in templates/. Templates are text/template with a styling FuncMap
// (color, bold, italic, markdown, wrap, indent, …) — swap them without
// recompiling to restyle the app.
package render

import (
	"embed"
	_ "embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"text/template"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/foobarto/stado/internal/tui/theme"
)

//go:embed templates/*.tmpl
var defaultTemplates embed.FS

// Renderer renders UI elements using loaded templates and a theme.
type Renderer struct {
	theme *theme.Theme
	tmpl  *template.Template
	// mdCache memoises per-width TermRenderer instances. Creating one
	// via glamour.NewTermRenderer parses the entire ansi style bundle
	// (chroma lexer init, theme compile) — easily 5-10ms. During a
	// streaming turn we re-render every block 10+ times/sec at a
	// stable viewport width, so caching by width is a big win.
	mdCache map[int]*glamour.TermRenderer
	mdMu    sync.Mutex
}

// New returns a Renderer using bundled templates.
func New(th *theme.Theme) (*Renderer, error) {
	return NewWithOverlay(th, "")
}

// NewWithOverlay returns a Renderer using bundled templates, with optional
// overrides loaded from overlayDir. Filenames must match bundled names
// ("message_user.tmpl" etc.); missing files fall back to the embedded default.
func NewWithOverlay(th *theme.Theme, overlayDir string) (*Renderer, error) {
	r := &Renderer{theme: th, mdCache: map[int]*glamour.TermRenderer{}}

	root := template.New("stado").Funcs(r.funcMap())
	if err := walkTemplates(root, defaultTemplates, "templates"); err != nil {
		return nil, err
	}
	if overlayDir != "" {
		overlay := os.DirFS(overlayDir)
		if err := walkTemplates(root, overlay, "."); err != nil {
			return nil, fmt.Errorf("render: overlay %s: %w", overlayDir, err)
		}
	}
	r.tmpl = root
	return r, nil
}

func walkTemplates(root *template.Template, fsys fs.FS, base string) error {
	return fs.WalkDir(fsys, base, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if filepath.Ext(path) != ".tmpl" {
			return nil
		}
		data, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(filepath.Base(path), ".tmpl")
		if _, err := root.New(name).Parse(string(data)); err != nil {
			return fmt.Errorf("render: parse %s: %w", path, err)
		}
		return nil
	})
}

// Exec runs the named template with the given data.
func (r *Renderer) Exec(name string, data any) (string, error) {
	var buf strings.Builder
	if err := r.tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return "", fmt.Errorf("render: exec %s: %w", name, err)
	}
	return strings.TrimRight(buf.String(), "\n") + "\n", nil
}

// ExecTo writes the rendered template to w.
func (r *Renderer) ExecTo(w io.Writer, name string, data any) error {
	return r.tmpl.ExecuteTemplate(w, name, data)
}

// Theme returns the underlying theme so callers can access layout knobs.
func (r *Renderer) Theme() *theme.Theme { return r.theme }

// SetTheme swaps the active theme while preserving compiled templates.
func (r *Renderer) SetTheme(th *theme.Theme) {
	if th == nil {
		return
	}
	r.theme = th
	r.mdMu.Lock()
	r.mdCache = map[int]*glamour.TermRenderer{}
	r.mdMu.Unlock()
}

// funcMap exposes styling helpers to templates. Keep this list stable — it's
// the public contract with anyone writing custom templates.
func (r *Renderer) funcMap() template.FuncMap {
	return template.FuncMap{
		// color "name" "text" — apply a themed foreground colour.
		"color": func(name string, text any) string {
			return r.theme.Fg(name).Render(toStr(text))
		},
		// bg "name" "text" — themed background.
		"bg": func(name string, text any) string {
			return r.theme.Bg(name).Render(toStr(text))
		},
		"bold": func(text any) string {
			return lipgloss.NewStyle().Bold(true).Render(toStr(text))
		},
		"italic": func(text any) string {
			return lipgloss.NewStyle().Italic(true).Render(toStr(text))
		},
		"underline": func(text any) string {
			return lipgloss.NewStyle().Underline(true).Render(toStr(text))
		},
		"muted": func(text any) string {
			return r.theme.Fg("muted").Render(toStr(text))
		},
		"wrap": func(text any, width int) string {
			return wordWrap(toStr(text), width)
		},
		"wrapHard": func(text any, width int) string {
			return hardWrap(toStr(text), width)
		},
		"indent": func(text any, n int) string {
			return indent(toStr(text), n)
		},
		"markdown": func(text any, width int) string {
			return r.markdown(toStr(text), width)
		},
		"marker": func(glyph, colorName string) string {
			return r.theme.Fg(colorName).Bold(true).Render(glyph)
		},
		"todoMarker": func(status string) string {
			switch status {
			case "done":
				return r.theme.Fg("success").Render("[v]")
			case "in_progress":
				return r.theme.Fg("warning").Render("[*]")
			default:
				return r.theme.Fg("muted").Render("[ ]")
			}
		},
		"todoColor": func(status string) string {
			switch status {
			case "done":
				return "muted"
			case "in_progress":
				return "warning"
			default:
				return "text"
			}
		},
		// railCard renders a flat inset block with a colored left rail,
		// closer to opencode's message panels than a terminal "box".
		"railCard": func(body any, width int, colorName string) string {
			w := width - 4
			if w < 10 {
				w = 10
			}
			content := wordWrap(toStr(body), w-2)
			return lipgloss.NewStyle().
				Background(r.theme.Bg("surface").GetBackground()).
				Border(lipgloss.Border{Left: "│"}, false, false, false, true).
				BorderForeground(r.theme.Fg(colorName).GetForeground()).
				Foreground(r.theme.Fg("text").GetForeground()).
				Padding(1, 1).
				Width(width).
				Render(content)
		},
		"cardUser": func(body any, width int) string {
			w := width - 4
			if w < 10 {
				w = 10
			}
			content := wordWrap(toStr(body), w-2)
			return lipgloss.NewStyle().
				Background(r.theme.Bg("surface").GetBackground()).
				Border(lipgloss.Border{Left: "│"}, false, false, false, true).
				BorderForeground(r.theme.Fg("role_user").GetForeground()).
				Foreground(r.theme.Fg("text").GetForeground()).
				Padding(1, 1).
				Width(width).
				Render(content)
		},
	}
}

func toStr(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case fmt.Stringer:
		return s.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", s)
	}
}

// markdown renders body through glamour at a given width. Falls back to raw
// text if glamour errors, so a template author never ends up with an empty
// assistant message. The per-width TermRenderer is memoised on r (protected
// by mdMu) — creating one costs ~5-10ms, which adds up fast when streaming.
func (r *Renderer) markdown(body string, width int) string {
	if width <= 0 {
		width = 80
	}
	r.mdMu.Lock()
	gm, ok := r.mdCache[width]
	if !ok {
		var err error
		style := styles.DarkStyle
		if themeUsesLightMarkdown(r.theme) {
			style = styles.LightStyle
		}
		gm, err = glamour.NewTermRenderer(
			// Avoid WithAutoStyle in the TUI: it queries terminal
			// background via OSC/CPR on first assistant markdown render,
			// racing Bubble Tea's input reader and freezing the UI.
			glamour.WithStandardStyle(style),
			glamour.WithWordWrap(width),
			glamour.WithPreservedNewLines(),
		)
		if err != nil {
			r.mdMu.Unlock()
			return body
		}
		r.mdCache[width] = gm
	}
	r.mdMu.Unlock()
	out, err := gm.Render(body)
	if err != nil {
		return body
	}
	return strings.TrimRight(out, "\n")
}

func themeUsesLightMarkdown(th *theme.Theme) bool {
	if th == nil {
		return false
	}
	bg := strings.TrimSpace(th.Colors.Background)
	bg = strings.TrimPrefix(bg, "#")
	if len(bg) != 6 {
		return false
	}
	r, errR := strconv.ParseUint(bg[0:2], 16, 8)
	g, errG := strconv.ParseUint(bg[2:4], 16, 8)
	b, errB := strconv.ParseUint(bg[4:6], 16, 8)
	if errR != nil || errG != nil || errB != nil {
		return false
	}
	luma := 0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)
	return luma >= 128
}

func wordWrap(s string, width int) string {
	if width <= 0 || width >= len(s) {
		return s
	}
	var lines []string
	for _, paragraph := range strings.Split(s, "\n") {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			lines = append(lines, "")
			continue
		}
		var line strings.Builder
		for _, w := range words {
			if line.Len() > 0 && line.Len()+1+len(w) > width {
				lines = append(lines, line.String())
				line.Reset()
			}
			if line.Len() > 0 {
				line.WriteByte(' ')
			}
			line.WriteString(w)
		}
		if line.Len() > 0 {
			lines = append(lines, line.String())
		}
	}
	return strings.Join(lines, "\n")
}

// hardWrap breaks at width boundaries without respecting word breaks — for
// narrow sidebar values like paths.
func hardWrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	var lines []string
	for _, ln := range strings.Split(s, "\n") {
		for len(ln) > width {
			lines = append(lines, ln[:width])
			ln = ln[width:]
		}
		lines = append(lines, ln)
	}
	return strings.Join(lines, "\n")
}

func indent(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = pad + ln
	}
	return strings.Join(lines, "\n")
}
