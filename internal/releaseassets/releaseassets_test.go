package releaseassets

import "testing"

func TestParseSHA256Sidecar(t *testing.T) {
	t.Parallel()

	got, err := ParseSHA256Sidecar([]byte(
		"fc87e78f7cb3fea12d69072e7ef3b21509754717b746368fd40d88963630e2b3  ripgrep-14.1.1-x86_64-apple-darwin.tar.gz\n",
	), "ripgrep-14.1.1-x86_64-apple-darwin.tar.gz")
	if err != nil {
		t.Fatalf("ParseSHA256Sidecar error = %v", err)
	}
	want := "fc87e78f7cb3fea12d69072e7ef3b21509754717b746368fd40d88963630e2b3"
	if got != want {
		t.Fatalf("ParseSHA256Sidecar = %q, want %q", got, want)
	}
}

func TestParseSHA256SidecarMatchesBasename(t *testing.T) {
	t.Parallel()

	got, err := ParseSHA256Sidecar([]byte(
		"24ad76777745fbff131c8fbc466742b011f925bfa4fffa2ded6def23b5b937be  deployment/m2/ripgrep-14.1.1-aarch64-apple-darwin.tar.gz\n",
	), "ripgrep-14.1.1-aarch64-apple-darwin.tar.gz")
	if err != nil {
		t.Fatalf("ParseSHA256Sidecar error = %v", err)
	}
	want := "24ad76777745fbff131c8fbc466742b011f925bfa4fffa2ded6def23b5b937be"
	if got != want {
		t.Fatalf("ParseSHA256Sidecar = %q, want %q", got, want)
	}
}

func TestParseSHA256SidecarWindowsCertUtilOutput(t *testing.T) {
	t.Parallel()

	got, err := ParseSHA256Sidecar([]byte(
		"SHA256 hash of ripgrep-14.1.1-x86_64-pc-windows-msvc.zip:\r\n"+
			"d0f534024c42afd6cb4d38907c25cd2b249b79bbe6cc1dbee8e3e37c2b6e25a1\r\n"+
			"CertUtil: -hashfile command completed successfully.\r\n",
	), "ripgrep-14.1.1-x86_64-pc-windows-msvc.zip")
	if err != nil {
		t.Fatalf("ParseSHA256Sidecar error = %v", err)
	}
	want := "d0f534024c42afd6cb4d38907c25cd2b249b79bbe6cc1dbee8e3e37c2b6e25a1"
	if got != want {
		t.Fatalf("ParseSHA256Sidecar = %q, want %q", got, want)
	}
}

func TestParseGitHubExpandedAssetsDigests(t *testing.T) {
	t.Parallel()

	body := []byte(`
<li class="Box-row">
  <a href="/ast-grep/ast-grep/releases/download/0.38.7/app-x86_64-apple-darwin.zip">
    <span class="Truncate-text text-bold">app-x86_64-apple-darwin.zip</span>
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
	if got["app-x86_64-apple-darwin.zip"] != want {
		t.Fatalf("digest = %q, want %q", got["app-x86_64-apple-darwin.zip"], want)
	}
	if _, ok := got["Source code"]; ok {
		t.Fatalf("source archive should not be included: %#v", got)
	}
}
