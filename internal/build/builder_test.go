package build

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/deploypier/deploypier/internal/config"
)

func TestBuildCopiesOnlyIncludedFiles(t *testing.T) {
	tempDir := t.TempDir()
	projectRoot := filepath.Join(tempDir, "project")
	if err := os.MkdirAll(filepath.Join(projectRoot, "app"), 0o755); err != nil {
		t.Fatalf("mkdir app: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, "node_modules"), 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "app", "main.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write app file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "node_modules", "skip.txt"), []byte("skip"), 0o644); err != nil {
		t.Fatalf("write excluded file: %v", err)
	}

	cfg := config.Config{
		Project: config.ProjectConfig{
			Name: "project",
			Root: projectRoot,
		},
		Build: config.BuildConfig{
			Include: []string{"app/**"},
			Exclude: []string{"node_modules/**"},
		},
		Release: config.ReleaseConfig{
			Directory: filepath.Join(tempDir, "releases"),
			Retain:    5,
		},
		State: config.StateConfig{
			File: filepath.Join(tempDir, "state.json"),
		},
		Transport: config.TransportConfig{
			Kind: "local",
			Path: filepath.Join(tempDir, "remote"),
		},
		Activation: config.ActivationConfig{
			Kind:           "pointer",
			CurrentPointer: filepath.Join(tempDir, "remote", "current.txt"),
		},
	}

	builder := NewBuilder()
	builder.Now = func() time.Time {
		return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	}

	release, err := builder.Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("build release: %v", err)
	}

	includedPath := filepath.Join(release.BundlePath, "app", "main.txt")
	if _, err := os.Stat(includedPath); err != nil {
		t.Fatalf("expected included file: %v", err)
	}

	excludedPath := filepath.Join(release.BundlePath, "node_modules", "skip.txt")
	if _, err := os.Stat(excludedPath); !os.IsNotExist(err) {
		t.Fatalf("expected excluded file to be absent, got err=%v", err)
	}

	if len(release.Manifest.Files) != 1 {
		t.Fatalf("expected 1 manifest file, got %d", len(release.Manifest.Files))
	}
	if release.Manifest.Files[0].Path != "app/main.txt" {
		t.Fatalf("unexpected manifest path: %s", release.Manifest.Files[0].Path)
	}
	if release.Manifest.Project != "project" {
		t.Fatalf("unexpected manifest project: %s", release.Manifest.Project)
	}
}

func TestBuildPrunesOlderRetainedReleases(t *testing.T) {
	tempDir := t.TempDir()
	projectRoot := filepath.Join(tempDir, "project")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "main.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write source file: %v", err)
	}

	releaseDir := filepath.Join(tempDir, "releases")
	for _, name := range []string{"20260714T100000Z", "20260714T110000Z"} {
		if err := os.MkdirAll(filepath.Join(releaseDir, name), 0o755); err != nil {
			t.Fatalf("mkdir retained release: %v", err)
		}
	}

	cfg := config.Config{
		Project: config.ProjectConfig{
			Name: "project",
			Root: projectRoot,
		},
		Release: config.ReleaseConfig{
			Directory: releaseDir,
			Retain:    2,
		},
		State: config.StateConfig{
			File: filepath.Join(tempDir, "state.json"),
		},
		Transport: config.TransportConfig{
			Kind: "local",
			Path: filepath.Join(tempDir, "remote"),
		},
		Activation: config.ActivationConfig{
			Kind:           "pointer",
			CurrentPointer: filepath.Join(tempDir, "remote", "current.txt"),
		},
	}

	builder := NewBuilder()
	builder.Now = func() time.Time {
		return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	}

	if _, err := builder.Build(context.Background(), cfg); err != nil {
		t.Fatalf("build release: %v", err)
	}

	if _, err := os.Stat(filepath.Join(releaseDir, "20260714T100000Z")); !os.IsNotExist(err) {
		t.Fatalf("expected oldest release to be pruned, got err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(releaseDir, "20260714T110000Z")); err != nil {
		t.Fatalf("expected newer retained release to remain: %v", err)
	}
	if _, err := os.Stat(filepath.Join(releaseDir, "20260714T120000Z")); err != nil {
		t.Fatalf("expected current release to remain: %v", err)
	}
}
