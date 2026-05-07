package lspfind

import (
	"strings"

	"github.com/foobarto/stado/internal/tools/budget"
)

const lspOutputHint = "narrow the query or inspect specific files"

func truncateLSPOutput(s string) string {
	s = strings.TrimRight(s, "\n")
	return budget.TruncateBytes(s, budget.LSPBytes, lspOutputHint)
}
