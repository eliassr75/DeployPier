package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/deploypier/deploypier/internal/activation"
	"github.com/deploypier/deploypier/internal/build"
	"github.com/deploypier/deploypier/internal/config"
	"github.com/deploypier/deploypier/internal/hooks"
	"github.com/deploypier/deploypier/internal/state"
	"github.com/deploypier/deploypier/internal/status"
	"github.com/deploypier/deploypier/internal/transport"
)

type fakeBuilder struct {
	buildRelease build.Release
	loadRelease  build.Release
}

func (f *fakeBuilder) Build(ctx context.Context, cfg config.Config) (build.Release, error) {
	_ = ctx
	_ = cfg
	return f.buildRelease, nil
}

func (f *fakeBuilder) Load(ctx context.Context, cfg config.Config, releaseID string) (build.Release, error) {
	_ = ctx
	_ = cfg
	if releaseID != "" && releaseID != f.loadRelease.ID {
		return build.Release{}, status.Wrap(status.KindNotFound, "load fake release", os.ErrNotExist)
	}
	return f.loadRelease, nil
}

type fakeTransport struct {
	uploaded []string
	files    map[string][]byte
}

func (f *fakeTransport) Name() string {
	return "fake"
}

func (f *fakeTransport) Probe(ctx context.Context) status.Report {
	_ = ctx
	return status.Report{Level: status.LevelOK, Code: "ok", Message: "ready"}
}

func (f *fakeTransport) UploadRelease(ctx context.Context, release build.Release, remotePath string) (transport.UploadResult, error) {
	_ = ctx
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.uploaded = append(f.uploaded, release.ID)
	for _, file := range release.Manifest.Files {
		content, err := os.ReadFile(filepath.Join(release.BundlePath, filepath.FromSlash(file.Path)))
		if err != nil {
			return transport.UploadResult{}, err
		}
		f.files[path.Join(remotePath, file.Path)] = content
	}
	rawManifest, err := json.Marshal(release.Manifest)
	if err != nil {
		return transport.UploadResult{}, err
	}
	f.files[path.Join(remotePath, "manifest.json")] = rawManifest
	return transport.UploadResult{RemotePath: remotePath, ManifestPath: path.Join(remotePath, "manifest.json")}, nil
}

func (f *fakeTransport) ReadFile(ctx context.Context, remotePath string) ([]byte, error) {
	_ = ctx
	if data, ok := f.files[remotePath]; ok {
		return append([]byte{}, data...), nil
	}
	return nil, os.ErrNotExist
}

func (f *fakeTransport) WriteFile(ctx context.Context, remotePath string, data []byte) error {
	_ = ctx
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.files[remotePath] = append([]byte{}, data...)
	return nil
}

func (f *fakeTransport) Stat(ctx context.Context, remotePath string) (transport.FileInfo, error) {
	_ = ctx
	data, ok := f.files[remotePath]
	if !ok {
		return transport.FileInfo{}, os.ErrNotExist
	}
	return transport.FileInfo{Path: remotePath, Size: int64(len(data))}, nil
}

func (f *fakeTransport) HashFile(ctx context.Context, remotePath string) (transport.FileInfo, error) {
	_ = ctx
	data, ok := f.files[remotePath]
	if !ok {
		return transport.FileInfo{}, os.ErrNotExist
	}
	sum := sha256.Sum256(data)
	return transport.FileInfo{
		Path:   remotePath,
		Size:   int64(len(data)),
		SHA256: hex.EncodeToString(sum[:]),
	}, nil
}

func (f *fakeTransport) ReadDir(ctx context.Context, remotePath string) ([]transport.FileInfo, error) {
	_ = ctx
	return []transport.FileInfo{}, nil
}

func (f *fakeTransport) Rename(ctx context.Context, fromPath string, toPath string) error {
	_ = ctx
	if data, ok := f.files[fromPath]; ok {
		f.files[toPath] = data
		delete(f.files, fromPath)
	}
	return nil
}

func (f *fakeTransport) Mkdir(ctx context.Context, remotePath string) error {
	_ = ctx
	_ = remotePath
	return nil
}

func (f *fakeTransport) MkdirAll(ctx context.Context, remotePath string) error {
	_ = ctx
	_ = remotePath
	return nil
}

func (f *fakeTransport) RemoveAll(ctx context.Context, remotePath string) error {
	_ = ctx
	delete(f.files, remotePath)
	return nil
}

func (f *fakeTransport) Exists(ctx context.Context, remotePath string) (bool, error) {
	_ = ctx
	_, ok := f.files[remotePath]
	return ok, nil
}

type fakeActivator struct {
	current   string
	previous  string
	activated []string
}

func (f *fakeActivator) Name() string {
	return "fake"
}

func (f *fakeActivator) Current(ctx context.Context) (string, error) {
	_ = ctx
	if f.current == "" {
		return "", status.Wrap(status.KindNotFound, "current", os.ErrNotExist)
	}
	return f.current, nil
}

func (f *fakeActivator) Previous(ctx context.Context) (string, error) {
	_ = ctx
	if f.previous == "" {
		return "", status.Wrap(status.KindNotFound, "previous", os.ErrNotExist)
	}
	return f.previous, nil
}

func (f *fakeActivator) Activate(ctx context.Context, releaseID string, reason string) (activation.Result, error) {
	_ = ctx
	_ = reason
	if f.current != "" && f.current != releaseID {
		f.previous = f.current
	}
	f.current = releaseID
	f.activated = append(f.activated, releaseID)
	return activation.Result{
		ReleaseID:   releaseID,
		ReleasePath: "/remote/releases/" + releaseID,
		PublicPath:  "/remote/public_html",
		Mode:        "release-based",
		Message:     "activated",
	}, nil
}

type fakeHooks struct {
	phases []string
}

func (f *fakeHooks) RunPhase(ctx context.Context, phase string, specs []config.HookSpec, metadata hooks.Metadata) ([]hooks.Result, error) {
	_ = ctx
	_ = specs
	_ = metadata
	f.phases = append(f.phases, phase)
	return nil, nil
}

func TestBuildRecordsReleaseAndRunsHooks(t *testing.T) {
	tempDir := t.TempDir()
	store := state.New(filepath.Join(tempDir, "state.json"))
	builder := &fakeBuilder{
		buildRelease: build.Release{
			ID:   "rel-1",
			Path: filepath.Join(tempDir, "releases", "rel-1"),
		},
	}
	hooksRunner := &fakeHooks{}
	service := &Service{
		cfg: config.Config{
			Project: config.ProjectConfig{Name: "demo"},
			Hooks: config.HooksConfig{
				BeforeBuild: []config.HookSpec{{Name: "before"}},
				AfterBuild:  []config.HookSpec{{Name: "after"}},
			},
		},
		builder: builder,
		store:   store,
		hooks:   hooksRunner,
		now: func() time.Time {
			return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
		},
	}

	release, err := service.Build(context.Background())
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	if release.ID != "rel-1" {
		t.Fatalf("unexpected release id: %s", release.ID)
	}

	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if len(snapshot.Builds) != 1 {
		t.Fatalf("expected 1 recorded build, got %d", len(snapshot.Builds))
	}
	if len(hooksRunner.phases) != 2 || hooksRunner.phases[0] != "before_build" || hooksRunner.phases[1] != "after_build" {
		t.Fatalf("unexpected hook phases: %#v", hooksRunner.phases)
	}
}

func TestPushUsesLatestBuildWhenReleaseIsOmitted(t *testing.T) {
	tempDir := t.TempDir()
	store := state.New(filepath.Join(tempDir, "state.json"))
	if err := store.RecordBuild(context.Background(), state.BuildRecord{
		ReleaseID: "rel-2",
		Path:      filepath.Join(tempDir, "releases", "rel-2"),
		BuiltAt:   "2026-07-14T12:00:00Z",
	}); err != nil {
		t.Fatalf("record build: %v", err)
	}

	builder := &fakeBuilder{
		loadRelease: build.Release{
			ID:   "rel-2",
			Path: filepath.Join(tempDir, "releases", "rel-2"),
		},
	}
	transportImpl := &fakeTransport{}
	activator := &fakeActivator{}
	hooksRunner := &fakeHooks{}
	service := &Service{
		cfg: config.Config{
			Hooks: config.HooksConfig{
				BeforePush:     []config.HookSpec{{Name: "before-push"}},
				AfterPush:      []config.HookSpec{{Name: "after-push"}},
				BeforeActivate: []config.HookSpec{{Name: "before-activate"}},
				AfterActivate:  []config.HookSpec{{Name: "after-activate"}},
			},
		},
		builder:   builder,
		store:     store,
		transport: transportImpl,
		activator: activator,
		hooks:     hooksRunner,
		now: func() time.Time {
			return time.Date(2026, 7, 14, 12, 30, 0, 0, time.UTC)
		},
	}

	result, err := service.Push(context.Background(), "", false)
	if err != nil {
		t.Fatalf("push service: %v", err)
	}
	if result.ReleaseID != "rel-2" {
		t.Fatalf("unexpected release id: %s", result.ReleaseID)
	}
	if !result.Activated {
		t.Fatalf("expected activation to happen")
	}
	if len(transportImpl.uploaded) != 1 || transportImpl.uploaded[0] != "rel-2" {
		t.Fatalf("unexpected uploaded releases: %#v", transportImpl.uploaded)
	}
	if len(activator.activated) != 1 || activator.activated[0] != "rel-2" {
		t.Fatalf("unexpected activated releases: %#v", activator.activated)
	}
}

func TestPushManualModeSignalsManualMigrationWhenMigrationsExist(t *testing.T) {
	tempDir := t.TempDir()
	store := state.New(filepath.Join(tempDir, "state.json"))

	release := build.Release{
		ID:   "rel-3",
		Path: filepath.Join(tempDir, "releases", "rel-3"),
	}
	if err := store.RecordBuild(context.Background(), state.BuildRecord{
		ReleaseID: release.ID,
		Path:      release.Path,
		BuiltAt:   "2026-07-15T10:00:00Z",
	}); err != nil {
		t.Fatalf("record build: %v", err)
	}

	projectRoot := filepath.Join(tempDir, "project")
	if err := os.MkdirAll(filepath.Join(projectRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, "database", "migrations"), 0o755); err != nil {
		t.Fatalf("mkdir migrations: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "database", "migrations", "2026_07_15_000000_create_widgets_table.php"), []byte(`<?php
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;
return new class {
    public function up(): void
    {
        Schema::create('widgets', function (Blueprint $table) {
            $table->id();
        });
    }
};`), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	writeGitStub(t, projectRoot, "database/migrations/2026_07_15_000000_create_widgets_table.php\n")
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", projectRoot+string(os.PathListSeparator)+originalPath)

	builder := &fakeBuilder{loadRelease: release}
	transportImpl := &fakeTransport{}
	activator := &fakeActivator{}
	hooksRunner := &fakeHooks{}
	service := &Service{
		cfg: config.Config{
			Project: config.ProjectConfig{Root: projectRoot},
			PostDeploy: config.PostDeployConfig{
				Mode: "manual",
			},
		},
		builder:   builder,
		store:     store,
		transport: transportImpl,
		activator: activator,
		hooks:     hooksRunner,
		now:       func() time.Time { return time.Date(2026, 7, 15, 10, 5, 0, 0, time.UTC) },
	}

	result, err := service.Push(context.Background(), release.ID, false)
	if err != nil {
		t.Fatalf("push service: %v", err)
	}

	if result.Status != "needs_manual_migration" {
		t.Fatalf("expected needs_manual_migration, got %s", result.Status)
	}
	if len(result.Warnings) == 0 {
		t.Fatalf("expected manual migration warning")
	}
}

func TestDoctorWarnsWhenPublicIndexIsNotReadyForCurrentPointerMode(t *testing.T) {
	transportImpl := &fakeTransport{
		files: map[string][]byte{
			"/remote/public_html/index.php": []byte("<?php $app->usePublicPath(__DIR__);"),
		},
	}
	service := &Service{
		cfg: config.Config{
			Remote: config.RemoteConfig{
				PublicRoot: "/remote/public_html",
			},
			Activation: config.ActivationConfig{
				CurrentPointer: "/remote/.deploypier/current.txt",
			},
		},
		transport: transportImpl,
		activator: &fakeActivator{},
	}

	checks, err := service.Doctor(context.Background())
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}

	check := findDoctorCheck(t, checks, "public_index")
	if check.Report.Level != status.LevelWarn {
		t.Fatalf("expected warning for public index, got %s", check.Report.Level)
	}
	if !strings.Contains(check.Report.Message, "current pointer mode") {
		t.Fatalf("unexpected message: %s", check.Report.Message)
	}
}

func findDoctorCheck(t *testing.T, checks []DoctorCheck, name string) DoctorCheck {
	t.Helper()
	for _, check := range checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("doctor check not found: %s", name)
	return DoctorCheck{}
}

func writeGitStub(t *testing.T, dir string, stdout string) {
	t.Helper()

	if os.PathSeparator == '\\' {
		gitScript := filepath.Join(dir, "git.cmd")
		content := "@echo off\r\n" + "echo " + stdout
		if err := os.WriteFile(gitScript, []byte(content), 0o644); err != nil {
			t.Fatalf("write git stub: %v", err)
		}
		return
	}

	gitScript := filepath.Join(dir, "git")
	content := "#!/bin/sh\nprintf '%s' \"" + stdout + "\"\n"
	if err := os.WriteFile(gitScript, []byte(content), 0o755); err != nil {
		t.Fatalf("write git stub: %v", err)
	}
}
