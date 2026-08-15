package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

const (
	testSourceCommitA = "0123456789abcdef0123456789abcdef01234567"
	testSourceCommitB = "89abcdef0123456789abcdef0123456789abcdef"
)

func TestResolveRemoteSourceCommitDereferencesAnnotatedGitHubTag(t *testing.T) {
	id, err := plugins.ParseIdentity("github.com/acme/plugins/supervise@v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	tagObject := "1111111111111111111111111111111111111111"
	nestedObject := "2222222222222222222222222222222222222222"
	oldGet := remoteSourceMetadataGet
	t.Cleanup(func() { remoteSourceMetadataGet = oldGet })
	var calls []string
	remoteSourceMetadataGet = func(_ context.Context, endpoint string) ([]byte, error) {
		calls = append(calls, endpoint)
		switch {
		case strings.Contains(endpoint, "/ref/tags/supervise%2Fv1.2.3"):
			return []byte(`{"object":{"type":"tag","sha":"` + tagObject + `"}}`), nil
		case strings.HasSuffix(endpoint, "/tags/"+tagObject):
			return []byte(`{"object":{"type":"tag","sha":"` + nestedObject + `"}}`), nil
		case strings.HasSuffix(endpoint, "/tags/"+nestedObject):
			return []byte(`{"object":{"type":"commit","sha":"` + testSourceCommitA + `"}}`), nil
		default:
			return nil, errors.New("unexpected endpoint: " + endpoint)
		}
	}

	got, err := resolveRemoteSourceCommit(id, "supervise/v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if got != testSourceCommitA || len(calls) != 3 {
		t.Fatalf("resolved commit=%q calls=%#v", got, calls)
	}
}

func TestResolveRemoteSourceCommitUsesGitLabTagCommit(t *testing.T) {
	id, err := plugins.ParseIdentity("gitlab.com/acme/plugins/supervise@v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	oldGet := remoteSourceMetadataGet
	t.Cleanup(func() { remoteSourceMetadataGet = oldGet })
	remoteSourceMetadataGet = func(_ context.Context, endpoint string) ([]byte, error) {
		if !strings.Contains(endpoint, "/projects/acme%2Fplugins/repository/tags/supervise%2Fv1.2.3") {
			t.Fatalf("endpoint = %q", endpoint)
		}
		return []byte(`{"commit":{"id":"` + testSourceCommitA + `"}}`), nil
	}
	got, err := resolveRemoteSourceCommit(id, "supervise/v1.2.3")
	if err != nil || got != testSourceCommitA {
		t.Fatalf("resolved commit=%q err=%v", got, err)
	}
}

func TestResolveRemoteSourceCommitCommitIdentityNeedsNoMetadata(t *testing.T) {
	id, err := plugins.ParseIdentity("github.com/acme/plugins/supervise@" + testSourceCommitA)
	if err != nil {
		t.Fatal(err)
	}
	oldGet := remoteSourceMetadataGet
	t.Cleanup(func() { remoteSourceMetadataGet = oldGet })
	remoteSourceMetadataGet = func(context.Context, string) ([]byte, error) {
		t.Fatal("immutable commit identity performed a metadata lookup")
		return nil, nil
	}
	got, err := resolveRemoteSourceCommit(id, testSourceCommitA)
	if err != nil || got != testSourceCommitA {
		t.Fatalf("resolved commit=%q err=%v", got, err)
	}
}

func TestResolveRemoteSourceCommitRejectsUnresolvableOrMalformedTag(t *testing.T) {
	unsupported, err := plugins.ParseIdentity("code.example/acme/plugins/supervise@v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveRemoteSourceCommit(unsupported, "supervise/v1.2.3"); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unsupported host error = %v", err)
	}

	github, err := plugins.ParseIdentity("github.com/acme/plugins/supervise@v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	oldGet := remoteSourceMetadataGet
	t.Cleanup(func() { remoteSourceMetadataGet = oldGet })
	remoteSourceMetadataGet = func(context.Context, string) ([]byte, error) {
		return []byte(`{"object":{"type":"commit","sha":"NOT-A-COMMIT"}}`), nil
	}
	if _, err := resolveRemoteSourceCommit(github, "supervise/v1.2.3"); err == nil || !strings.Contains(err.Error(), "invalid object id") {
		t.Fatalf("malformed github response error = %v", err)
	}
}

func TestTryRawTreeFetchUsesDereferencedCommitNotMutableTag(t *testing.T) {
	id, err := plugins.ParseIdentity("github.com/acme/plugins/supervise@v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	oldDownload := remotePluginArtifactDownload
	t.Cleanup(func() { remotePluginArtifactDownload = oldDownload })
	var sources []string
	remotePluginArtifactDownload = func(source, _ string) error {
		sources = append(sources, source)
		return nil
	}
	if err := tryRawTreeFetch(id, testSourceCommitA, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if len(sources) != 3 {
		t.Fatalf("download sources = %#v", sources)
	}
	for _, source := range sources {
		if !strings.Contains(source, "/"+testSourceCommitA+"/supervise/dist/") || strings.Contains(source, "v1.2.3") {
			t.Fatalf("raw fetch did not use exact commit: %q", source)
		}
	}
}

func TestCheckRemotePackageContinuityRefusesTagRewriteUnlessExplicit(t *testing.T) {
	cfg := isolatedHome(t)
	id, err := plugins.ParseIdentity("github.com/acme/plugins/supervise@v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	lockPath := pluginLockPath(cfg, false)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := plugins.Manifest{Name: "supervise", Version: "1.2.3", WASMSHA256: "wasm"}
	entry, err := plugins.LockEntryFromResolvedManifest(id, "supervise/v1.2.3", testSourceCommitA, manifest)
	if err != nil {
		t.Fatal(err)
	}
	lock := plugins.NewLock()
	lock.Add(entry)
	if err := lock.Write(lockPath); err != nil {
		t.Fatal(err)
	}

	if err := checkRemotePackageContinuity(cfg, false, id, "supervise/v1.2.3", testSourceCommitA, manifest, false); err != nil {
		t.Fatalf("unchanged source rejected: %v", err)
	}
	if err := checkRemotePackageContinuity(cfg, false, id, "supervise/v1.2.3", testSourceCommitB, manifest, false); err == nil || !strings.Contains(err.Error(), "tag rewrite detected") {
		t.Fatalf("tag rewrite error = %v", err)
	}
	if err := checkRemotePackageContinuity(cfg, false, id, "supervise/v1.2.3", testSourceCommitB, manifest, true); err != nil {
		t.Fatalf("explicitly accepted tag rewrite rejected: %v", err)
	}
}

func TestCheckRemotePackageContinuityRefusesSameCommitPackageRewrite(t *testing.T) {
	cfg := isolatedHome(t)
	id, err := plugins.ParseIdentity("github.com/acme/plugins/supervise@v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	lockPath := pluginLockPath(cfg, false)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	locked := plugins.Manifest{Name: "supervise", Version: "1.2.3", WASMSHA256: "wasm-a"}
	entry, err := plugins.LockEntryFromResolvedManifest(id, "supervise/v1.2.3", testSourceCommitA, locked)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&plugins.Lock{Entries: []plugins.LockEntry{entry}}).Write(lockPath); err != nil {
		t.Fatal(err)
	}
	rewritten := locked
	rewritten.Author = "replacement"
	if err := checkRemotePackageContinuity(cfg, false, id, "supervise/v1.2.3", testSourceCommitA, rewritten, false); err == nil || !strings.Contains(err.Error(), "signed package rewrite detected") {
		t.Fatalf("same-commit package rewrite error = %v", err)
	}
	if err := checkRemotePackageContinuity(cfg, false, id, "supervise/v1.2.3", testSourceCommitA, rewritten, true); err != nil {
		t.Fatalf("explicitly accepted package rewrite rejected: %v", err)
	}
}

func TestCheckRemotePackageContinuityFailsClosedForLegacyOrDuplicateLock(t *testing.T) {
	cfg := isolatedHome(t)
	id, err := plugins.ParseIdentity("github.com/acme/plugins/supervise@v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	lockPath := pluginLockPath(cfg, false)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := plugins.Manifest{Name: "supervise", Version: "1.2.3", WASMSHA256: "wasm"}
	legacy := plugins.LockEntry{Identity: id.Canonical(), SourceRevision: "supervise/v1.2.3", PackageVersion: "1.2.3", WASMSHA256: "wasm"}
	if err := (&plugins.Lock{Entries: []plugins.LockEntry{legacy}}).Write(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := checkRemotePackageContinuity(cfg, false, id, "supervise/v1.2.3", testSourceCommitA, manifest, false); err == nil || !strings.Contains(err.Error(), "no source-keyed store key") {
		t.Fatalf("legacy lock error = %v", err)
	}
	if err := (&plugins.Lock{Entries: []plugins.LockEntry{legacy, legacy}}).Write(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := checkRemotePackageContinuity(cfg, false, id, "supervise/v1.2.3", testSourceCommitA, manifest, true); err == nil || !strings.Contains(err.Error(), "no source-keyed store key") {
		t.Fatalf("duplicate lock error = %v", err)
	}
}

func TestCheckRemotePackageContinuityNeverMovesCommitIdentity(t *testing.T) {
	cfg := isolatedHome(t)
	id, err := plugins.ParseIdentity("github.com/acme/plugins/supervise@" + testSourceCommitA)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := pluginLockPath(cfg, false)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := plugins.Manifest{Name: "supervise", Version: "1.2.3", WASMSHA256: "wasm"}
	entry, err := plugins.LockEntryFromResolvedManifest(id, testSourceCommitA, testSourceCommitA, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&plugins.Lock{Entries: []plugins.LockEntry{entry}}).Write(lockPath); err != nil {
		t.Fatal(err)
	}
	if err := checkRemotePackageContinuity(cfg, false, id, testSourceCommitA, testSourceCommitB, manifest, true); err == nil || !strings.Contains(err.Error(), "immutable commit identity") {
		t.Fatalf("commit movement error = %v", err)
	}
}

func TestRecordRemotePluginLockSerializesConflictingTagInstalls(t *testing.T) {
	cfg := isolatedHome(t)
	id, err := plugins.ParseIdentity("github.com/acme/plugins/supervise@v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	manifest := plugins.Manifest{Name: "supervise", Version: "1.2.3", WASMSHA256: "wasm"}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, commit := range []string{testSourceCommitA, testSourceCommitB} {
		commit := commit
		go func() {
			<-start
			errs <- recordRemotePluginLock(cfg, false, id, "supervise/v1.2.3", commit, manifest, false)
		}()
	}
	close(start)
	first, second := <-errs, <-errs
	successes := 0
	rewrites := 0
	for _, result := range []error{first, second} {
		switch {
		case result == nil:
			successes++
		case strings.Contains(result.Error(), "tag rewrite detected"):
			rewrites++
		default:
			t.Fatalf("unexpected concurrent lock result: %v", result)
		}
	}
	if successes != 1 || rewrites != 1 {
		t.Fatalf("concurrent results: success=%d rewrite=%d (%v, %v)", successes, rewrites, first, second)
	}
	lock, err := plugins.ReadLock(pluginLockPath(cfg, false))
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Entries) != 1 {
		t.Fatalf("conflicting tag installs committed %d rows: %+v", len(lock.Entries), lock.Entries)
	}
}
