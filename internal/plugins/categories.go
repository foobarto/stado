package plugins

import "fmt"

// CanonicalCategories is the frozen taxonomy from EP-0037 §C.
// Extend only via a new EP that amends this list.
var CanonicalCategories = []string{
	"filesystem", "shell", "network", "web",
	"dns", "crypto", "data", "encoding",
	"code-search", "code-edit", "lsp", "agent",
	"task", "mcp", "image", "secrets",
	"documentation", "ctf-offense", "ctf-recon", "ctf-postex",
	"meta",
}

var canonicalSet = func() map[string]bool {
	m := make(map[string]bool, len(CanonicalCategories))
	for _, c := range CanonicalCategories {
		m[c] = true
	}
	return m
}()

// ValidateCategories returns an error if any entry is not in the canonical
// taxonomy. Empty slice is valid (tool won't appear in in_category results).
func ValidateCategories(cats []string) error {
	for _, c := range cats {
		if !canonicalSet[c] {
			return fmt.Errorf("unknown category %q; canonical categories: %v", c, CanonicalCategories)
		}
	}
	return nil
}
