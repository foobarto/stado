package releaseassets

import "testing"

func TestParseSHA256Sidecar(t *testing.T) {
	t.Parallel()

	got, err := ParseSHA256Sidecar([]byte(
		"4cf9f2741e6c465ffdb7c26f38056a59e2a2544b51f7cc128ef28337eeae4d8e  ripgrep-14.1.1-x86_64-unknown-linux-musl.tar.gz\n",
	), "ripgrep-14.1.1-x86_64-unknown-linux-musl.tar.gz")
	if err != nil {
		t.Fatalf("ParseSHA256Sidecar error = %v", err)
	}
	want := "4cf9f2741e6c465ffdb7c26f38056a59e2a2544b51f7cc128ef28337eeae4d8e"
	if got != want {
		t.Fatalf("ParseSHA256Sidecar = %q, want %q", got, want)
	}
}

func TestParseSHA256SidecarMatchesBasename(t *testing.T) {
	t.Parallel()

	got, err := ParseSHA256Sidecar([]byte(
		"c827481c4ff4ea10c9dc7a4022c8de5db34a5737cb74484d62eb94a95841ab2f  deployment/linux/ripgrep-14.1.1-aarch64-unknown-linux-gnu.tar.gz\n",
	), "ripgrep-14.1.1-aarch64-unknown-linux-gnu.tar.gz")
	if err != nil {
		t.Fatalf("ParseSHA256Sidecar error = %v", err)
	}
	want := "c827481c4ff4ea10c9dc7a4022c8de5db34a5737cb74484d62eb94a95841ab2f"
	if got != want {
		t.Fatalf("ParseSHA256Sidecar = %q, want %q", got, want)
	}
}

func TestParseGitHubExpandedAssetsDigests(t *testing.T) {
	t.Parallel()

	body := []byte(`
<li class="Box-row">
  <a href="/ast-grep/ast-grep/releases/download/0.38.7/app-x86_64-unknown-linux-gnu.zip">
    <span class="Truncate-text text-bold">app-x86_64-unknown-linux-gnu.zip</span>
  </a>
  <span class="Truncate-text">sha256:add804dc5c0575038fd8cc2549629246dc08c83d074cd1e464224360c62a031d</span>
</li>
<li class="Box-row">
  <a href="/ast-grep/ast-grep/archive/refs/tags/0.38.7.zip">
    <span class="Truncate-text text-bold">Source code</span>
    <span class="Truncate-text">(zip)</span>
  </a>
</li>`)

	got, err := ParseGitHubExpandedAssetsDigests(body)
	if err != nil {
		t.Fatalf("ParseGitHubExpandedAssetsDigests error = %v", err)
	}

	const want = "add804dc5c0575038fd8cc2549629246dc08c83d074cd1e464224360c62a031d"
	if got["app-x86_64-unknown-linux-gnu.zip"] != want {
		t.Fatalf("digest = %q, want %q", got["app-x86_64-unknown-linux-gnu.zip"], want)
	}
	if _, ok := got["Source code"]; ok {
		t.Fatalf("source archive should not be included: %#v", got)
	}
}
