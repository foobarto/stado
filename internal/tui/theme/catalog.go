package theme

import "fmt"

// CatalogEntry describes one bundled TUI theme.
type CatalogEntry struct {
	ID          string
	Name        string
	Mode        string
	Description string
}

var lightTOML = []byte(`
name = "stado-light"

[colors]
background      = "#f6f7f8"
surface         = "#e9ecef"
border          = "#c7ced6"
primary         = "#2f7a5f"
accent          = "#315fba"
muted           = "#68736d"
success         = "#2f7a5f"
warning         = "#8a6d1f"
error           = "#aa3d3d"

text            = "#121817"
text_dim        = "#5d6762"
text_secondary  = "#34413c"

role_user       = "#315fba"
role_assistant  = "#121817"
role_thinking   = "#527365"
role_tool       = "#2f6f96"
role_system     = "#aa3d3d"

[layout]
sidebar_width     = 28
sidebar_min_width = 24
border_style      = "normal"
padding           = 1
message_indent    = 2

[markdown]
style = "auto"
`)

var contrastTOML = []byte(`
name = "stado-contrast"

[colors]
background      = "#000000"
surface         = "#111111"
border          = "#777777"
primary         = "#00ff66"
accent          = "#00b7ff"
muted           = "#a0a0a0"
success         = "#00ff66"
warning         = "#ffd84d"
error           = "#ff5f5f"

text            = "#f2f2f2"
text_dim        = "#c7c7c7"
text_secondary  = "#ffffff"

role_user       = "#00b7ff"
role_assistant  = "#f2f2f2"
role_thinking   = "#8cffb7"
role_tool       = "#00b7ff"
role_system     = "#ff5f5f"

[layout]
sidebar_width     = 28
sidebar_min_width = 24
border_style      = "normal"
padding           = 1
message_indent    = 2

[markdown]
style = "auto"
`)

var catalog = []CatalogEntry{
	{ID: "stado-dark", Name: "Stado Dark", Mode: "dark", Description: "Default dark theme"},
	{ID: "stado-light", Name: "Stado Light", Mode: "light", Description: "Bright neutral theme"},
	{ID: "stado-contrast", Name: "Stado Contrast", Mode: "dark", Description: "High-contrast dark theme"},
}

// Catalog returns the bundled themes in display order.
func Catalog() []CatalogEntry {
	out := make([]CatalogEntry, len(catalog))
	copy(out, catalog)
	return out
}

// BuiltinTOML returns the source TOML for a bundled theme.
func BuiltinTOML(id string) ([]byte, bool) {
	var data []byte
	switch id {
	case "stado-dark":
		data = defaultTOML
	case "stado-light":
		data = lightTOML
	case "stado-contrast":
		data = contrastTOML
	default:
		return nil, false
	}
	out := make([]byte, len(data))
	copy(out, data)
	return out, true
}

// Named loads a bundled theme by id.
func Named(id string) (*Theme, error) {
	data, ok := BuiltinTOML(id)
	if !ok {
		return nil, fmt.Errorf("theme: unknown bundled theme %q", id)
	}
	th, err := parse(data)
	if err != nil {
		return nil, err
	}
	if th.Name == "" {
		th.Name = id
	}
	return th, nil
}
