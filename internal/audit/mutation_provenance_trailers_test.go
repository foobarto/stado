package audit_test

import (
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/foobarto/stado/internal/audit"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// commitMessageFor writes a single trace commit for meta and returns its
// rendered message — the canonical bytes the audit parser later reads.
func commitMessageFor(t *testing.T, meta stadogit.CommitMeta) string {
	t.Helper()
	sc, err := stadogit.OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	if err != nil {
		t.Fatalf("sidecar: %v", err)
	}
	sess, err := stadogit.CreateSession(sc, t.TempDir(), "sess-prov", plumbing.ZeroHash)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	h, err := sess.CommitToTrace(meta)
	if err != nil {
		t.Fatalf("CommitToTrace: %v", err)
	}
	c, err := object.GetCommit(sc.Repo().Storer, h)
	if err != nil {
		t.Fatalf("GetCommit: %v", err)
	}
	return c.Message
}

// TestMutationProvenanceTrailers_RenderOnlyWhenSet (STAGE 1): the four
// new CommitMeta fields are zero-valued by default and must NOT emit
// trailers when empty, but must round-trip through audit.ParseMessage
// when set. Purely additive — no existing trailer changes.
func TestMutationProvenanceTrailers_RenderOnlyWhenSet(t *testing.T) {
	// Default (all four empty): no provenance trailers present.
	bare := commitMessageFor(t, stadogit.CommitMeta{Tool: "grep", Summary: "search"})
	_, bareTrailers := audit.ParseMessage(bare)
	for _, k := range []string{"Original-Result-SHA", "Mutated-By-Hook", "Deny-Reason", "Denied-By-Hook"} {
		if _, ok := bareTrailers[k]; ok {
			t.Errorf("zero-valued meta must not emit %s trailer; message:\n%s", k, bare)
		}
	}

	// All four set: each round-trips through ParseMessage with its value.
	full := commitMessageFor(t, stadogit.CommitMeta{
		Tool:              "stubread",
		Summary:           "non-mutating [ok]",
		ResultSHA:         "sha256:mutated",
		OriginalResultSHA: "sha256:original",
		MutatedByHook:     "redact",
		DenyReason:        "blocked by policy",
		DeniedByHook:      "guard",
	})
	_, fullTrailers := audit.ParseMessage(full)
	cases := map[string]string{
		"Result-SHA":          "sha256:mutated",
		"Original-Result-SHA": "sha256:original",
		"Mutated-By-Hook":     "redact",
		"Deny-Reason":         "blocked by policy",
		"Denied-By-Hook":      "guard",
	}
	for k, want := range cases {
		if got := fullTrailers[k]; got != want {
			t.Errorf("trailer %s = %q, want %q", k, got, want)
		}
	}
}

// TestMutationProvenanceTrailers_InjectionDefense (STAGE 1): a
// model-influenceable hook name / deny reason carrying embedded newlines
// must NOT inject extra trailer lines — it routes through
// cleanTrailerValue like every other untrusted value.
func TestMutationProvenanceTrailers_InjectionDefense(t *testing.T) {
	msg := commitMessageFor(t, stadogit.CommitMeta{
		Tool:          "stubread",
		Summary:       "non-mutating [ok]",
		MutatedByHook: "evil\nTool: bash\nAgent: legit",
		DenyReason:    "x\nResult-SHA: sha256:forged",
	})
	_, trailers := audit.ParseMessage(msg)
	// The injected `Tool: bash` / forged Result-SHA must NOT win.
	if trailers["Tool"] != "stubread" {
		t.Errorf("injected Tool trailer overrode the real value: %q", trailers["Tool"])
	}
	if got := trailers["Result-SHA"]; got == "sha256:forged" {
		t.Errorf("deny-reason injection forged a Result-SHA trailer: %q", got)
	}
	if trailers["Agent"] == "legit" {
		t.Errorf("hook-name injection forged an Agent trailer")
	}
}
