package headless

import (
	"sort"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
)

type toolInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Class       string `json:"class"`
}

func (s *Server) toolsList() (any, error) {
	exec, err := runtime.BuildExecutor(nil, s.Cfg, "stado-headless")
	if err != nil {
		return nil, &acp.RPCError{Code: acp.CodeInternalError, Message: err.Error()}
	}
	all := exec.Registry.All()
	out := make([]toolInfo, 0, len(all))
	for _, t := range all {
		cls := exec.Registry.ClassOf(t.Name()).String()
		out = append(out, toolInfo{
			Name:        t.Name(),
			Description: t.Description(),
			Class:       cls,
		})
	}
	return out, nil
}

func availableProviders(cfg *config.Config) []string {
	// Source of truth is the provider registry — iterate it rather than
	// hard-coding names so a new bundled provider shows up here without a
	// second edit. "gemini" is the accepted alias for "google" and isn't a
	// registry entry, so add it explicitly.
	set := map[string]struct{}{"gemini": {}}
	for _, p := range config.KnownProviders() {
		set[p.Name] = struct{}{}
	}
	if cfg != nil {
		for name, preset := range cfg.Inference.Presets {
			if preset.Endpoint != "" {
				set[name] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
