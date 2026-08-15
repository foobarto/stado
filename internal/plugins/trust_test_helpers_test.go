package plugins

// testTrustNamespace keeps low-level trust/signature tests focused on their
// intended invariant while production callers must supply host-authenticated
// source identity. It is deliberately test-only.
func testTrustNamespace(manifest *Manifest) string {
	if manifest == nil || manifest.Name == "" {
		return "test.dev/plugins/unknown"
	}
	return "test.dev/plugins/" + manifest.Name
}
