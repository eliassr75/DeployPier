package activation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deploypier/deploypier/internal/config"
	"github.com/deploypier/deploypier/internal/transport"
)

func TestPointerActivatorReleaseBasedSwapAndState(t *testing.T) {
	tempDir := t.TempDir()
	remoteRoot := filepath.Join(tempDir, "remote")
	appRoot := filepath.Join(remoteRoot, "app")
	publicRoot := filepath.Join(remoteRoot, "public_html")
	pointerPath := filepath.Join(remoteRoot, ".deploypier", "current.txt")

	makeRelease := func(id string, asset string) {
		releaseRoot := filepath.Join(appRoot, "releases", id)
		mustMkdirAll(t, filepath.Join(releaseRoot, "bootstrap"))
		mustMkdirAll(t, filepath.Join(releaseRoot, "vendor"))
		mustMkdirAll(t, filepath.Join(releaseRoot, "public", "build"))
		mustWriteFile(t, filepath.Join(releaseRoot, "bootstrap", "app.php"), []byte("<?php return new class { public function usePublicPath($path) {} };"))
		mustWriteFile(t, filepath.Join(releaseRoot, "vendor", "autoload.php"), []byte("<?php"))
		mustWriteFile(t, filepath.Join(releaseRoot, "public", "build", "app.js"), []byte(asset))
	}

	makeRelease("rel-1", "first-build")
	makeRelease("rel-2", "second-build")

	mustMkdirAll(t, publicRoot)
	mustWriteFile(t, filepath.Join(publicRoot, "index.php"), []byte(`<?php
$pointerFile = '/tmp/.deploypier/current.txt';
$releaseId = trim((string) file_get_contents($pointerFile));
$releaseRoot = '/tmp/app/releases/'.$releaseId;
$app->usePublicPath(__DIR__);
`))
	mustWriteFile(t, filepath.Join(publicRoot, "storage"), []byte("preserve-me"))

	fs := &transport.LocalTransport{BasePath: remoteRoot}
	activator, err := New(
		config.ActivationConfig{
			Kind:           "pointer",
			CurrentPointer: pointerPath,
		},
		config.RemoteConfig{
			AppRoot:    appRoot,
			PublicRoot: publicRoot,
			Layout:     "release-based",
		},
		fs,
	)
	if err != nil {
		t.Fatalf("new activator: %v", err)
	}

	pointer := activator.(*PointerActivator)
	pointer.Now = func() time.Time {
		return time.Date(2026, 7, 15, 18, 0, 0, 0, time.UTC)
	}

	result, err := activator.Activate(context.Background(), "rel-1", "push")
	if err != nil {
		t.Fatalf("activate rel-1: %v", err)
	}
	if result.Mode != "release-based" {
		t.Fatalf("unexpected mode: %s", result.Mode)
	}
	if _, err := os.Stat(filepath.Join(publicRoot, "build", "app.js")); err != nil {
		t.Fatalf("expected public asset after activation: %v", err)
	}
	if raw, err := os.ReadFile(pointerPath); err != nil || string(raw) != "rel-1\n" {
		t.Fatalf("unexpected current pointer: %q err=%v", string(raw), err)
	}
	if raw, err := os.ReadFile(filepath.Join(publicRoot, "storage")); err != nil || string(raw) != "preserve-me" {
		t.Fatalf("expected storage path to be preserved")
	}

	if _, err := activator.Activate(context.Background(), "rel-2", "push"); err != nil {
		t.Fatalf("activate rel-2: %v", err)
	}
	current, err := activator.Current(context.Background())
	if err != nil || current != "rel-2" {
		t.Fatalf("unexpected current release: %s err=%v", current, err)
	}
	previous, err := activator.Previous(context.Background())
	if err != nil || previous != "rel-1" {
		t.Fatalf("unexpected previous release: %s err=%v", previous, err)
	}
}

func mustMkdirAll(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
