// Package render drives template-based rendering for the TUI.
//
// Each visual element (message, tool call, sidebar, status bar) has a .tmpl
// file in templates/. Templates are text/template with a styling FuncMap
// (color, bold, italic, markdown, wrap, indent, …) — swap them without
// recompiling to restyle the app.
package render

import (
	_ "embed"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/foobarto/stado/internal/tui/theme"
)

//go:embed templates/*.tmpl
var defaultTemplates embed.FS

// Renderer renders UI elements using loaded templates and a theme.
type Renderer struct {
	theme    *theme.Theme
	glamour  *glamour.TermRenderer
	tmpl     *template.Template
}

// New returns a Renderer using bundled templates.
func New(th *theme.Theme) (*Renderer, error) {
	return NewWithOverlay(th, "")
}

// NewWithOverlay returns a Renderer using bundled templates, with optional
// overrides loaded from overlayDir. Filenames must match bundled names
// ("message_user.tmpl" etc.); missing files fall back to the embedded default.
func NewWithOverlay(th *theme.Theme, overlayDir string) (*Renderer, error) {
	r := &Renderer{theme: th}

	gm, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(0), // caller controls width
	)
	if err != nil {
		return nil, fmt.Errorf("render: glamour: %w", err)
	}
	r.glamour = gm

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
// assistant message.
func (r *Renderer) markdown(body string, width int) string {
	if width <= 0 {
		width = 80
	}
	gm, err := glamour.NewTermRenderer(
		glamour.WithAutoStyle(),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return body
	}
	out, err := gm.Render(body)
	if err != nil {
		return body
	}
	return strings.TrimRight(out, "\n")
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
