package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

func installBrokerLocalPlugin(t *testing.T, name string) (*config.Config, string, plugins.RuntimeIdentity, plugins.Manifest) {
	t.Helper()
	cfg := isolatedHome(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	src := buildTestPlugin(t, priv, pub, name, "1.0.0")
	oldSigner, oldForce := pluginInstallSigner, pluginInstallForce
	oldAutoload, oldLocal, oldAnchor := pluginInstallAutoload, pluginInstallLocal, pluginInstallTrustAnchor
	pluginInstallSigner = hex.EncodeToString(pub)
	pluginInstallForce, pluginInstallAutoload, pluginInstallLocal, pluginInstallTrustAnchor = false, false, false, false
	t.Cleanup(func() {
		pluginInstallSigner, pluginInstallForce = oldSigner, oldForce
		pluginInstallAutoload, pluginInstallLocal, pluginInstallTrustAnchor = oldAutoload, oldLocal, oldAnchor
	})
	if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src}); err != nil {
		t.Fatalf("install local development plugin: %v", err)
	}
	sourceManifest, _, err := plugins.LoadFromDir(src)
	if err != nil {
		t.Fatal(err)
	}
	record, err := plugins.NewLocalInstallRecord(src, *sourceManifest)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg.StateDir(), "plugins", record.StoreKey)
	manifest, _, err := plugins.LoadFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := plugins.RuntimeIdentityForInstalledDir(dir, *manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(identity.Namespace, "local://sha256/") {
		t.Fatalf("installed local identity = %#v", identity)
	}
	return cfg, dir, identity, *manifest
}

func TestBrokerVerifierAdmitsExactSignedLocalDevelopmentInstall(t *testing.T) {
	cfg, _, identity, manifest := installBrokerLocalPlugin(t, "supervise-dev")
	gotIdentity, gotManifest, err := (brokerInstalledPluginVerifier{cfg: cfg}).VerifyArtifactPlugin(context.Background(), identity, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if gotIdentity != identity {
		t.Fatalf("verified identity = %#v, want %#v", gotIdentity, identity)
	}
	gotDigest, err := gotManifest.ManifestDigest()
	if err != nil || gotDigest != identity.ManifestDigest {
		t.Fatalf("verified manifest digest = %q err=%v, want %q", gotDigest, err, identity.ManifestDigest)
	}
}

func TestBrokerVerifierRejectsTamperedOrUntrustedLocalDevelopmentInstall(t *testing.T) {
	t.Run("wasm tamper", func(t *testing.T) {
		cfg, dir, identity, manifest := installBrokerLocalPlugin(t, "tampered-dev")
		if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), []byte("tampered"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := (brokerInstalledPluginVerifier{cfg: cfg}).VerifyArtifactPlugin(context.Background(), identity, manifest); err == nil || !strings.Contains(err.Error(), "wasm") {
			t.Fatalf("tampered wasm error = %v", err)
		}
	})

	t.Run("signer removed", func(t *testing.T) {
		cfg, _, identity, manifest := installBrokerLocalPlugin(t, "untrusted-dev")
		if err := plugins.NewTrustStore(cfg.StateDir()).Untrust(manifest.AuthorPubkeyFpr); err != nil {
			t.Fatal(err)
		}
		if _, _, err := (brokerInstalledPluginVerifier{cfg: cfg}).VerifyArtifactPlugin(context.Background(), identity, manifest); err == nil || !strings.Contains(err.Error(), "not pinned") {
			t.Fatalf("untrusted signer error = %v", err)
		}
	})

	t.Run("manifest mismatch", func(t *testing.T) {
		cfg, _, identity, manifest := installBrokerLocalPlugin(t, "mismatch-dev")
		manifest.Author = "substituted"
		if _, _, err := (brokerInstalledPluginVerifier{cfg: cfg}).VerifyArtifactPlugin(context.Background(), identity, manifest); err == nil || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("manifest mismatch error = %v", err)
		}
	})
}

func TestBrokerVerifierRejectsSymlinkedLocalInstallDirectory(t *testing.T) {
	cfg, dir, identity, manifest := installBrokerLocalPlugin(t, "linked-dev")
	outside := filepath.Join(t.TempDir(), "installed-copy")
	if err := os.Rename(dir, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	if _, _, err := (brokerInstalledPluginVerifier{cfg: cfg}).VerifyArtifactPlugin(context.Background(), identity, manifest); err == nil || !strings.Contains(err.Error(), "is a symlink") {
		t.Fatalf("symlinked install error = %v", err)
	}
}

func TestBrokerVerifierAdmitsExactRemoteLockIdentity(t *testing.T) {
	cfg := isolatedHome(t)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	src := buildTestPlugin(t, priv, pub, "remote-supervise", "1.0.0")
	manifest, signature, err := plugins.LoadFromDir(src)
	if err != nil {
		t.Fatal(err)
	}
	id, err := plugins.ParseIdentity("github.com/acme/stado-plugins/remote-supervise@v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	entry, err := plugins.LockEntryFromResolvedManifest(id, "remote-supervise/v1.0.0", "0123456789abcdef0123456789abcdef01234567", *manifest)
	if err != nil {
		t.Fatal(err)
	}
	record, err := plugins.NewRemoteInstallRecord(id, entry.SourceRevision, entry.ResolvedCommit, *manifest)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg.StateDir(), "plugins", record.StoreKey)
	if err := copyDir(src, dir); err != nil {
		t.Fatal(err)
	}
	if err := plugins.WriteInstallRecord(dir, record, *manifest); err != nil {
		t.Fatal(err)
	}
	if err := plugins.WriteInstallReceipt(cfg.StateDir(), filepath.Join(cfg.StateDir(), "plugins"), record); err != nil {
		t.Fatal(err)
	}
	if _, err := plugins.NewTrustStore(cfg.StateDir()).TrustVerifiedAnchor(hex.EncodeToString(pub), "acme", id.Namespace(), id.OwnerKey(), manifest, signature); err != nil {
		t.Fatal(err)
	}
	lock := plugins.NewLock()
	lock.Add(entry)
	if err := lock.Write(filepath.Join(cfg.StateDir(), "plugin-lock.toml")); err != nil {
		t.Fatal(err)
	}
	identity, err := plugins.RuntimeIdentityForInstalledDir(dir, *manifest)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := (brokerInstalledPluginVerifier{cfg: cfg}).VerifyArtifactPlugin(context.Background(), identity, *manifest)
	if err != nil {
		t.Fatal(err)
	}
	if got != identity || got.SourceRevision != "remote-supervise/v1.0.0" {
		t.Fatalf("verified remote identity = %#v, want %#v", got, identity)
	}
}
