// Package localdetect probes the bundled local OAI-compat endpoints
// (ollama, llamacpp, vllm, lmstudio) with a short-timeout GET on
// /v1/models. The result tells stado which local runners are up right
// now + what models they have loaded — so `stado doctor` can surface a
// "you've got lmstudio running at localhost:1234 with llama-3.3:70b
// loaded, want to use it?" row without requiring API-key setup.
//
// Offline-safe: failures are probe timeouts, not errors bubbled up.
// Probes run concurrently — total wall time stays under the per-probe
// timeout.
package localdetect

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

var probeHTTPClient = &http.Client{Timeout: DefaultTimeout}

// BundledLocal is the set of local-runner presets stado probes by
// default. Matches tui/app.go's builtinPreset entries where keyEnv == "".
var BundledLocal = []Target{
	{Name: "ollama", Endpoint: "http://localhost:11434/v1"},
	{Name: "llamacpp", Endpoint: "http://localhost:8080/v1"},
	{Name: "vllm", Endpoint: "http://localhost:8000/v1"},
	{Name: "lmstudio", Endpoint: "http://localhost:1234/v1"},
}

// Target is one endpoint to probe.
type Target struct {
	Name     string
	Endpoint string
}

// Result is what a single probe produced.
type Result struct {
	Name      string
	Endpoint  string
	Reachable bool
	Err       error    // populated when Reachable is false
	Models    []string // sorted; empty when Reachable is false
}

// DefaultTimeout is the per-probe budget. A down server at localhost
// should fail within ~100ms; a real running server responds in ~10ms.
// 1s gives slack for slow loopback + cold caches.
const DefaultTimeout = 1 * time.Second

// Detect runs every Target concurrently and returns their Results in
// the same order as the input. Unreachable targets are included so
// callers can render "ollama: down" alongside "lmstudio: up (3 models)".
func Detect(ctx context.Context, targets []Target) []Result {
	results := make([]Result, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t Target) {
			defer wg.Done()
			results[i] = probeOne(ctx, t)
		}(i, t)
	}
	wg.Wait()
	return results
}

// DetectBundled is Detect over the BundledLocal set.
func DetectBundled(ctx context.Context) []Result {
	return Detect(ctx, BundledLocal)
}

// MergeUserPresets combines BundledLocal with user-defined
// [inference.presets.<name>] entries (endpoint set). A user preset
// whose name matches a bundled one overrides the bundled endpoint —
// same precedence as buildProvider's ordering, so doctor probes see
// the same endpoint the TUI will dial. Only http://localhost (and
// 127.0.0.1 / 0.0.0.0 / [::1]) endpoints are added beyond the bundled
// set; remote endpoints are hosted services (groq / openrouter / …)
// that aren't "local runners" for autodetect purposes.
//
// userPresets is typically cfg.Inference.Presets; the map is passed as
// name→endpoint so this package doesn't pull in the config type.
func MergeUserPresets(userPresets map[string]string) []Target {
	byName := map[string]Target{}
	order := []string{}
	for _, t := range BundledLocal {
		byName[t.Name] = t
		order = append(order, t.Name)
	}
	for name, ep := range userPresets {
		if ep == "" {
			continue
		}
		_, existed := byName[name]
		if !existed && !isLocalEndpoint(ep) {
			// Skip remote endpoints unless they override a bundled name.
			continue
		}
		if !existed {
			order = append(order, name)
		}
		byName[name] = Target{Name: name, Endpoint: ep}
	}
	out := make([]Target, 0, len(order))
	for _, n := range order {
		out = append(out, byName[n])
	}
	return out
}

func isLocalEndpoint(ep string) bool {
	u, err := url.Parse(ep)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

// probeOne hits {endpoint}/models with a short timeout. OAI-compat
// servers (ollama, vllm, llamacpp, lmstudio) all implement this path.
func probeOne(ctx context.Context, t Target) Result {
	out := Result{Name: t.Name, Endpoint: t.Endpoint}

	pctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(pctx, "GET", t.Endpoint+"/models", nil)
	if err != nil {
		out.Err = err
		return out
	}
	req.Header.Set("User-Agent", "stado-localdetect")

	resp, err := probeHTTPClient.Do(req)
	if err != nil {
		out.Err = err
		return out
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		out.Err = fmt.Errorf("HTTP %d", resp.StatusCode)
		return out
	}

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		out.Err = fmt.Errorf("decode: %w", err)
		return out
	}

	out.Reachable = true
	for _, m := range body.Data {
		if m.ID != "" {
			out.Models = append(out.Models, m.ID)
		}
	}
	sort.Strings(out.Models)
	return out
}
