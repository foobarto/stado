package webfetch

// Metadata for the webfetch tool. The Run method lives in webfetch_run.go
// (`!airgap`) or webfetch_run_airgap.go (`airgap`) so the network path
// is stripped from airgap builds.

type WebFetchTool struct{}

func (WebFetchTool) Name() string { return "webfetch" }
func (WebFetchTool) Description() string {
	return "Fetch the contents of a URL and convert it to markdown"
}
func (WebFetchTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{"type": "string", "description": "The URL to fetch"},
		},
		"required": []string{"url"},
	}
}

type Args struct {
	URL string `json:"url"`
}
