package runtime

// Supported lifecycle-application surfaces (EP-0064, cleanup C26).
//
// A surface is not application-capable merely because it eventually calls
// AgentLoop or can instantiate a WASM tool. Support means it owns one durable
// instance for the exact (canonical plugin, session, generation) tuple and
// routes commands, tools, hooks, events, ticks, close, and every required host
// bridge through that same serialized instance. For v0.80/v1 only the
// interactive TUI satisfies that whole contract.

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

type ApplicationSurface string

const (
	ApplicationSurfaceTUI      ApplicationSurface = "tui"
	ApplicationSurfaceRun      ApplicationSurface = "run"
	ApplicationSurfaceHeadless ApplicationSurface = "headless"
	ApplicationSurfaceACP      ApplicationSurface = "acp"
)

type ApplicationSurfaceContract struct {
	PersistentInstances bool
	Commands            bool
	Tools               bool
	Hooks               bool
	Events              bool
	GenericBridges      bool
}

func LifecycleApplicationSurfaceContract(surface ApplicationSurface) ApplicationSurfaceContract {
	if surface == ApplicationSurfaceTUI {
		return ApplicationSurfaceContract{
			PersistentInstances: true, Commands: true, Tools: true,
			Hooks: true, Events: true, GenericBridges: true,
		}
	}
	return ApplicationSurfaceContract{}
}

func (c ApplicationSurfaceContract) Complete() bool {
	return c.PersistentInstances && c.Commands && c.Tools && c.Hooks && c.Events && c.GenericBridges
}

// ConfiguredLifecycleApplication is classification metadata only. Executing
// the package still requires the normal digest, signature, trust, capability,
// and broker-admission checks in LoadInstalledLifecycleApplication.
type ConfiguredLifecycleApplication struct {
	ConfiguredID string
	CanonicalID  string
	Directory    string
}

// ConfiguredLifecycleApplications resolves lifecycle declarations from the
// launch configuration plus surface-selected persona plugins. Unknown IDs are
// left to the ordinary background-plugin diagnostics; an installed package
// whose manifest cannot be classified fails closed because silently treating a
// malformed lifecycle package as a legacy background plugin would recreate the
// duplicate-instance bug this contract prevents.
func ConfiguredLifecycleApplications(cfg *config.Config, additionalIDs []string) ([]ConfiguredLifecycleApplication, error) {
	if cfg == nil {
		return nil, nil
	}
	ids := append([]string(nil), cfg.Plugins.Background...)
	ids = append(ids, additionalIDs...)
	seenID := make(map[string]struct{}, len(ids))
	seenCanonical := make(map[string]string, len(ids))
	var applications []ConfiguredLifecycleApplication
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, duplicate := seenID[id]; duplicate {
			continue
		}
		seenID[id] = struct{}{}
		if _, legacyBundled := LookupBackgroundPlugin(id); legacyBundled {
			continue
		}
		dir, err := plugins.InstalledDirInAny(lifecyclePluginRoots(cfg), id)
		if err != nil {
			return nil, fmt.Errorf("classify configured plugin %s: %w", id, err)
		}
		if info, statErr := os.Lstat(dir); statErr != nil {
			if os.IsNotExist(statErr) {
				continue
			}
			return nil, fmt.Errorf("classify configured plugin %s: %w", id, statErr)
		} else if !info.IsDir() {
			return nil, fmt.Errorf("classify configured plugin %s: install path is not a directory", id)
		}
		manifest, _, err := plugins.LoadFromDir(dir)
		if err != nil {
			return nil, fmt.Errorf("classify configured plugin %s: %w", id, err)
		}
		if manifest.Lifecycle == nil {
			continue
		}
		identity, err := RuntimeIdentityForPluginDir(dir, *manifest)
		if err != nil {
			return nil, fmt.Errorf("classify lifecycle application %s: %w", id, err)
		}
		if previous, duplicate := seenCanonical[identity.Canonical]; duplicate {
			return nil, fmt.Errorf("lifecycle application canonical identity %s is configured more than once (%s, %s)", identity.Canonical, previous, id)
		}
		seenCanonical[identity.Canonical] = id
		applications = append(applications, ConfiguredLifecycleApplication{
			ConfiguredID: id, CanonicalID: identity.Canonical, Directory: dir,
		})
	}
	sort.Slice(applications, func(i, j int) bool { return applications[i].CanonicalID < applications[j].CanonicalID })
	return applications, nil
}

// RequireLifecycleApplicationSurface prevents partial application execution.
// Unsupported surfaces fail before provider/session work and direct operators
// to the one surface that owns the complete v1 composition contract.
func RequireLifecycleApplicationSurface(cfg *config.Config, additionalIDs []string, surface ApplicationSurface) error {
	applications, err := ConfiguredLifecycleApplications(cfg, additionalIDs)
	if err != nil {
		return err
	}
	if len(applications) == 0 || LifecycleApplicationSurfaceContract(surface).Complete() {
		return nil
	}
	canonical := make([]string, len(applications))
	for i, application := range applications {
		canonical[i] = application.CanonicalID
	}
	return fmt.Errorf("lifecycle application(s) %s are configured, but the %s surface does not host persistent lifecycle applications or their commands; use the interactive TUI (`stado`) for this session", strings.Join(canonical, ", "), surface)
}
