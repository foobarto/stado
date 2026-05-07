package lspfind

// find_references.go: FindReferences moved into find_definition.go alongside
// the shared package-level cache + helpers. This file is kept empty to
// preserve the import-path locality for callers that still reference
// "internal/lspfind/find_references"; logic lives in find_definition.go.
