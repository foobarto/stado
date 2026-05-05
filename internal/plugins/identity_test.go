package plugins_test

import (
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

func TestParseIdentity(t *testing.T) {
	cases := []struct {
		raw        string
		wantHost   string
		wantOwner  string
		wantRepo   string
		wantSubdir string
		wantVer    string
	}{
		{
			raw:      "github.com/foobarto/my-plugin@v1.2.3",
			wantHost: "github.com", wantOwner: "foobarto", wantRepo: "my-plugin", wantVer: "v1.2.3",
		},
		{
			raw:        "github.com/foobarto/monorepo/plugins/myplugin@v0.1.0",
			wantHost:   "github.com", wantOwner: "foobarto", wantRepo: "monorepo",
			wantSubdir: "plugins/myplugin", wantVer: "v0.1.0",
		},
		{
			raw:      "github.com/foo/bar@abc123def456abc123def456abc123def456abc1",
			wantHost: "github.com", wantOwner: "foo", wantRepo: "bar",
			wantVer: "abc123def456abc123def456abc123def456abc1",
		},
	}
	for _, c := range cases {
		id, err := plugins.ParseIdentity(c.raw)
		if err != nil {
			t.Errorf("ParseIdentity(%q) error: %v", c.raw, err)
			continue
		}
		if id.Host != c.wantHost {
			t.Errorf("%q host: got %q want %q", c.raw, id.Host, c.wantHost)
		}
		if id.Owner != c.wantOwner {
			t.Errorf("%q owner: got %q want %q", c.raw, id.Owner, c.wantOwner)
		}
		if id.Repo != c.wantRepo {
			t.Errorf("%q repo: got %q want %q", c.raw, id.Repo, c.wantRepo)
		}
		if id.Subdir != c.wantSubdir {
			t.Errorf("%q subdir: got %q want %q", c.raw, id.Subdir, c.wantSubdir)
		}
		if id.Version != c.wantVer {
			t.Errorf("%q version: got %q want %q", c.raw, id.Version, c.wantVer)
		}
	}
}

func TestParseIdentity_FloatingVersionsRejected(t *testing.T) {
	bad := []string{
		"github.com/foo/bar@latest",
		"github.com/foo/bar@main",
		"github.com/foo/bar@HEAD",
		"github.com/foo/bar@develop",
		"github.com/foo/bar",
	}
	for _, raw := range bad {
		if _, err := plugins.ParseIdentity(raw); err == nil {
			t.Errorf("ParseIdentity(%q) should fail", raw)
		}
	}
}

func TestIdentityKey_Stable(t *testing.T) {
	id1, _ := plugins.ParseIdentity("github.com/foo/bar@v1.0.0")
	id2, _ := plugins.ParseIdentity("github.com/foo/bar@v1.0.0")
	if id1.Key() != id2.Key() {
		t.Error("same identity should produce same key")
	}
	id3, _ := plugins.ParseIdentity("github.com/foo/bar@v2.0.0")
	if id1.Key() == id3.Key() {
		t.Error("different version should produce different key")
	}
}

func TestIdentityCanonical(t *testing.T) {
	id, _ := plugins.ParseIdentity("github.com/foo/bar/sub@v1.0.0")
	want := "github.com/foo/bar/sub@v1.0.0"
	if id.Canonical() != want {
		t.Errorf("Canonical() = %q, want %q", id.Canonical(), want)
	}
}
