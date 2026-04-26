// Package localdetect probes the bundled local OAI-compat endpoints
// (ollama, llamacpp, vllm, lmstudio) with a short-timeout GET on
// /v1/models. For LM Studio, which lists installed models even when none
// are loaded, it also probes the local API for loaded-state details. The
// result tells stado which local runners are up right now + which models
// are immediately runnable, so `stado doctor` can surface useful setup
// rows without requiring API-key setup.
//
// Offline-safe: failures are probe timeouts, not errors bubbled up.
// Probes run concurrently — total wall time stays under the per-probe
// timeout.
package localdetect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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
	Name           string
	Endpoint       string
	Reachable      bool
	Err            error    // populated when Reachable is false
	Models         []string // sorted; empty when Reachable is false
	LoadStateKnown bool     // true when the runner reports loaded vs installed state
	LoadedModels   []string // sorted; only populated when LoadStateKnown is true
}

// DefaultTimeout is the per-probe budget. A down server at localhost
// should fail within ~100ms; a real running server responds in ~10ms.
// 1s gives slack for slow loopback + cold caches.
const DefaultTimeout = 1 * time.Second

const maxModelListResponseBytes int64 = 1 << 20

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
	if err := decodeModelListJSON(resp.Body, &body); err != nil {
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
	if strings.EqualFold(t.Name, "lmstudio") {
		if loaded, ok := probeLMStudioLoadedModels(pctx, t.Endpoint); ok {
			out.LoadStateKnown = true
			out.LoadedModels = loaded
		}
	}
	return out
}

// RunnableModels returns the model ids that are immediately usable for chat.
// Most OAI-compatible runners expose only loaded/runnable models through
// /v1/models. LM Studio exposes installed models there, so when its richer
// API reports loaded state we use that narrower set.
func (r Result) RunnableModels() []string {
	if r.LoadStateKnown {
		return r.LoadedModels
	}
	return r.Models
}

func probeLMStudioLoadedModels(ctx context.Context, endpoint string) ([]string, bool) {
	req, err := http.NewRequestWithContext(ctx, "GET", lmStudioAPIBase(endpoint)+"/api/v0/models", nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", "stado-localdetect")
	resp, err := probeHTTPClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var body struct {
		Data []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"data"`
	}
	if err := decodeModelListJSON(resp.Body, &body); err != nil {
		return nil, false
	}
	var loaded []string
	for _, m := range body.Data {
		if m.ID != "" && strings.EqualFold(m.State, "loaded") {
			loaded = append(loaded, m.ID)
		}
	}
	sort.Strings(loaded)
	return loaded, true
}

func decodeModelListJSON(r io.Reader, v any) error {
	data, err := io.ReadAll(io.LimitReader(r, maxModelListResponseBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > maxModelListResponseBytes {
		return fmt.Errorf("model list response exceeds %d bytes", maxModelListResponseBytes)
	}
	return json.Unmarshal(data, v)
}

func lmStudioAPIBase(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return strings.TrimRight(endpoint, "/")
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/v1")
	u.RawQuery = ""
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}
