package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/foobarto/stado/pkg/agent"
)

type recordingProviderSessionBridge struct {
	mu       sync.Mutex
	calls    int
	identity string
	request  ProviderInvokeRequest
	ceiling  int
	facts    ProviderInvokeFacts
}

func (*recordingProviderSessionBridge) NextEvent(context.Context) ([]byte, error) { return nil, nil }
func (*recordingProviderSessionBridge) ReadField(string) ([]byte, error)          { return nil, nil }
func (*recordingProviderSessionBridge) Fork(context.Context, string, string) (string, error) {
	return "", nil
}
func (b *recordingProviderSessionBridge) InvokeProvider(_ context.Context, identity string, request ProviderInvokeRequest, ceiling int) (ProviderInvokeFacts, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	b.identity = identity
	b.request = request
	b.ceiling = ceiling
	return b.facts, nil
}

func TestProviderImportExactForwardingIdentityAndAccounting(t *testing.T) {
	temperature := 0.0
	request := ProviderInvokeRequest{
		System: "system", Messages: []ProviderInvokeMessage{{Role: "user", Content: "hello"}},
		Model: "model-x", MaxOutputTokens: 5, Temperature: &temperature,
	}
	bridge := &recordingProviderSessionBridge{facts: ProviderInvokeFacts{
		Schema: ProviderInvokeFactsSchemaV1, Status: "completed", Text: "answer",
		Provider: "provider-x", Model: "model-x",
		Usage: ProviderInvokeUsage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7},
	}}
	h := newBridgeHarness(t).withCaps("provider:invoke:100").withSessionBridge(bridge).install()
	h.host.Identity.Canonical = "github.com/example/plugin@v1.2.3"

	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	h.memWrite(0, raw)
	const outPtr, outCap = 4096, 4096
	n := h.callImport(context.Background(), "stado_provider_invoke", 0, uint64(len(raw)), outPtr, outCap)
	if n <= 0 {
		t.Fatalf("provider import returned %d", n)
	}
	var facts ProviderInvokeFacts
	if err := json.Unmarshal(h.memRead(outPtr, uint32(n)), &facts); err != nil {
		t.Fatal(err)
	}
	if facts.Status != "completed" || facts.Text != "answer" {
		t.Fatalf("facts = %+v", facts)
	}
	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	if bridge.identity != h.host.Identity.Canonical {
		t.Fatalf("identity = %q, want host identity %q", bridge.identity, h.host.Identity.Canonical)
	}
	// Strict input upper bound is base framing 32 + system/model bytes 13 +
	// message framing 16 + role/content bytes 9; requested output is 5.
	if bridge.ceiling != 75 {
		t.Fatalf("reserved ceiling = %d, want 75", bridge.ceiling)
	}
	if bridge.request.Temperature == nil || *bridge.request.Temperature != 0 {
		t.Fatalf("explicit zero temperature was not preserved: %+v", bridge.request)
	}
	if h.host.providerTokensUsed != 7 || h.host.providerTokensReserved != 0 {
		t.Fatalf("budget used=%d reserved=%d", h.host.providerTokensUsed, h.host.providerTokensReserved)
	}
}

func TestProviderImportAccountsAndSuppressesBridgeOverrun(t *testing.T) {
	request := ProviderInvokeRequest{
		Messages: []ProviderInvokeMessage{{Role: "user", Content: "x"}}, MaxOutputTokens: 5,
	}
	reservation := conservativeProviderInputTokens(request) + request.MaxOutputTokens
	bridge := &recordingProviderSessionBridge{facts: ProviderInvokeFacts{
		Schema: ProviderInvokeFactsSchemaV1, Status: "completed", Text: "must not cross ABI",
		Usage: ProviderInvokeUsage{InputTokens: reservation, OutputTokens: 1, TotalTokens: reservation + 1},
	}}
	h := newBridgeHarness(t).withCaps("provider:invoke:100").withSessionBridge(bridge).install()
	raw := mustProviderJSON(t, request)
	h.memWrite(0, raw)
	n := h.callImport(context.Background(), "stado_provider_invoke", 0, uint64(len(raw)), 4096, 4096)
	if n <= 0 {
		t.Fatalf("provider import returned %d", n)
	}
	var facts ProviderInvokeFacts
	if err := json.Unmarshal(h.memRead(4096, uint32(n)), &facts); err != nil {
		t.Fatal(err)
	}
	if facts.Status != "failed" || facts.Text != "" || facts.Diagnostic == nil || facts.Diagnostic.Kind != "token_budget" {
		t.Fatalf("over-budget bridge facts crossed ABI: %+v", facts)
	}
	if facts.Usage.TotalTokens != reservation+1 || h.host.providerTokensUsed != reservation+1 || h.host.providerTokensReserved != 0 {
		t.Fatalf("overrun accounting facts=%+v used=%d reserved=%d", facts.Usage, h.host.providerTokensUsed, h.host.providerTokensReserved)
	}
}

func TestProviderImportDeniedWithoutExactCapability(t *testing.T) {
	bridge := &recordingProviderSessionBridge{}
	h := newBridgeHarness(t).withSessionBridge(bridge).install()
	raw := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	h.memWrite(0, raw)
	if got := h.callImport(context.Background(), "stado_provider_invoke", 0, uint64(len(raw)), 4096, 4096); got != -1 {
		t.Fatalf("got %d, want -1", got)
	}
	if bridge.calls != 0 {
		t.Fatal("provider bridge called without capability")
	}
}

func TestProviderImportConservativeInputBoundDeniesBeforeBridgeDispatch(t *testing.T) {
	request := ProviderInvokeRequest{Messages: []ProviderInvokeMessage{{Role: "user", Content: "éx"}}}
	ceiling := conservativeProviderInputTokens(request)
	bridge := &recordingProviderSessionBridge{}
	h := newBridgeHarness(t).withCaps(fmt.Sprintf("provider:invoke:%d", ceiling)).withSessionBridge(bridge).install()
	raw := mustProviderJSON(t, request)
	h.memWrite(0, raw)
	if got := h.callImport(context.Background(), "stado_provider_invoke", 0, uint64(len(raw)), 4096, 4096); got != -1 {
		t.Fatalf("input equal to signed ceiling returned %d", got)
	}
	if bridge.calls != 0 {
		t.Fatal("bridge dispatched after conservative multibyte input consumed the signed ceiling")
	}
}

func TestDecodeProviderInvokeRequestStrictBounds(t *testing.T) {
	validZero := 0.0
	if _, err := decodeProviderInvokeRequest(mustProviderJSON(t, ProviderInvokeRequest{
		Messages: []ProviderInvokeMessage{{Role: "user", Content: "x"}}, Temperature: &validZero,
	})); err != nil {
		t.Fatalf("explicit zero temperature rejected: %v", err)
	}
	tests := []struct {
		name string
		raw  string
	}{
		{"unknown", `{"messages":[{"role":"user","content":"x"}],"actor":"guest"}`},
		{"trailing", `{"messages":[{"role":"user","content":"x"}]} {}`},
		{"authority", `{"messages":[{"role":"user","content":"x"}],"token_budget":1}`},
		{"bad-role", `{"messages":[{"role":"tool","content":"x"}]}`},
		{"empty-content", `{"messages":[{"role":"user","content":""}]}`},
		{"temperature", `{"messages":[{"role":"user","content":"x"}],"temperature":2.1}`},
		{"negative-output", `{"messages":[{"role":"user","content":"x"}],"max_output_tokens":-1}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeProviderInvokeRequest([]byte(test.raw)); err == nil {
				t.Fatal("expected strict rejection")
			}
		})
	}
	tooLarge := ProviderInvokeRequest{Messages: []ProviderInvokeMessage{{Role: "user", Content: strings.Repeat("x", maxProviderInvokeMessageBytes+1)}}}
	if _, err := decodeProviderInvokeRequest(mustProviderJSON(t, tooLarge)); err == nil {
		t.Fatal("oversized message accepted")
	}
}

func TestProviderUsageMergesPartialReportAndCacheTokens(t *testing.T) {
	usage := providerUsage(ProviderInvokeRequest{}, 5, "12345678", &agent.Usage{
		OutputTokens: 3, CacheReadTokens: 7, CacheWriteTokens: 11,
	}, false)
	if usage.InputTokens != 5 || usage.OutputTokens != 3 || usage.CacheReadTokens != 7 || usage.CacheWriteTokens != 11 || usage.TotalTokens != 8 {
		t.Fatalf("partial usage merge = %+v", usage)
	}
	if usage.Estimated {
		t.Fatalf("native-counted input plus reported output should be exact: %+v", usage)
	}

	if got := saturatingTokenSum(math.MaxInt, 1, math.MaxInt); got != math.MaxInt {
		t.Fatalf("saturating sum = %d, want MaxInt", got)
	}
}

func TestProviderBudgetReservationsAreRaceSafe(t *testing.T) {
	host := &Host{ProviderInvokeBudget: 100}
	req := ProviderInvokeRequest{MaxOutputTokens: 9}
	const callers = 32
	start := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	var reserveWG sync.WaitGroup
	var admittedMu sync.Mutex
	admitted := 0
	wg.Add(callers)
	reserveWG.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-start
			reservation, ok := host.reserveProviderTokens(req, 1)
			reserveWG.Done()
			if !ok {
				return
			}
			admittedMu.Lock()
			admitted++
			admittedMu.Unlock()
			<-release
			host.commitProviderTokens(reservation, 2)
		}()
	}
	close(start)
	reserveWG.Wait()
	host.providerBudgetMu.Lock()
	reserved := host.providerTokensReserved
	host.providerBudgetMu.Unlock()
	if reserved != 100 {
		t.Fatalf("reserved = %d, want 100", reserved)
	}
	close(release)
	wg.Wait()
	if admitted != 10 {
		t.Fatalf("admitted = %d, want 10", admitted)
	}
	if host.providerTokensReserved != 0 || host.providerTokensUsed != 20 {
		t.Fatalf("used=%d reserved=%d", host.providerTokensUsed, host.providerTokensReserved)
	}
}

func TestConservativeProviderInputBoundIsSaturatingAndStructural(t *testing.T) {
	req := ProviderInvokeRequest{
		System: strings.Repeat("s", 9), Model: "m",
		Messages: []ProviderInvokeMessage{{Role: "user", Content: "é" + strings.Repeat("x", 7)}},
	}
	wantMinimum := len(req.System) + len(req.Model) + len(req.Messages[0].Role) + len(req.Messages[0].Content) + 32 + 16
	if got := conservativeProviderInputTokens(req); got != wantMinimum {
		t.Fatalf("conservative input = %d, want structural one-byte/token bound %d", got, wantMinimum)
	}
	huge := ProviderInvokeRequest{Messages: []ProviderInvokeMessage{{Role: "user", Content: "x"}}}
	if got := conservativeProviderInputTokens(huge); got <= len(huge.Messages[0].Content) {
		t.Fatalf("conservative input omitted framing: %d", got)
	}
}

func TestProviderInvokeCancellationReturnsFactsAndUsage(t *testing.T) {
	provider := cancellingProvider{}
	bridge := NativeProviderBridge{Provider: provider, DefaultModel: "fake"}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	facts, err := bridge.InvokeProvider(ctx, "source.example/plugin@v1", ProviderInvokeRequest{
		Messages: []ProviderInvokeMessage{{Role: "user", Content: "hello"}},
	}, 100)
	if err != nil || facts.Status != "cancelled" || facts.Diagnostic == nil || facts.Diagnostic.Kind != "context_cancelled" {
		t.Fatalf("facts=%+v err=%v", facts, err)
	}
	if facts.Usage.TotalTokens == 0 {
		t.Fatalf("cancelled provider usage was not committed: %+v", facts.Usage)
	}
}

type cancellingProvider struct{}

func (cancellingProvider) Name() string                     { return "cancel" }
func (cancellingProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (cancellingProvider) StreamTurn(ctx context.Context, _ agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, 1)
	go func() {
		<-ctx.Done()
		ch <- agent.Event{Kind: agent.EvError, Err: ctx.Err()}
		close(ch)
	}()
	return ch, nil
}

func mustProviderJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
