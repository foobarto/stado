package keys

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
)

func Parse(input string, name string) []key.Binding {
	if input == "" || input == "none" {
		return nil
	}

	parts := strings.Split(input, ",")
	var bindings []key.Binding
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// OpenCode bindings use some aliases we need to translate for bubbles/key
		p = translateKey(p)
		bindings = append(bindings, key.NewBinding(
			key.WithKeys(p),
			key.WithHelp(p, name),
		))
	}
	return bindings
}

func translateKey(k string) string {
	k = strings.ToLower(k)
	switch k {
	case "return":
		return "enter"
	case "pageup":
		return "pgup"
	case "pagedown":
		return "pgdown"
	}
	return k
}
