package rg

import "os"

// envRG reads the STADO_RG env var. Kept in its own file so tests can swap
// via build tags or so future config plumbing can replace it.
func envRG() string { return os.Getenv("STADO_RG") }
