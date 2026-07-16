package app

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
	"github.com/deploypier/deploypier/internal/postdeploy"
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
	uploaded  []string
	files     map[string][]byte
	inspect   transport.Inspection
	removeErr error
}

func (f *fakeTransport) Name() string {
	return "fake"
}

func (f *fakeTransport) Probe(ctx context.Context) status.Report {
	_ = ctx
	return status.Report{Level: status.LevelOK, Code: "ok", Message: "ready"}
}

func (f *fakeTransport) Inspect(ctx context.Context) (transport.Inspection, error) {
	_ = ctx
	return f.inspect, nil
}

func (f *fakeTransport) UploadRelease(ctx context.Context, release build.Release, remotePath string, progress transport.UploadProgressFunc) (transport.UploadResult, error) {
	_ = ctx
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	f.uploaded = append(f.uploaded, release.ID)
	if strings.TrimSpace(release.ArchivePath) != "" {
		return f.uploadArchiveRelease(release, remotePath, progress)
	}
	totalFiles := len(release.Manifest.Files) + 1
	uploadedFiles := 0
	var uploadedBytes int64
	var totalBytes int64
	for _, file := range release.Manifest.Files {
		totalBytes += file.Size
	}
	for _, file := range release.Manifest.Files {
		content, err := os.ReadFile(filepath.Join(release.BundlePath, filepath.FromSlash(file.Path)))
		if err != nil {
			return transport.UploadResult{}, err
		}
		f.files[path.Join(remotePath, file.Path)] = content
		uploadedFiles++
		uploadedBytes += file.Size
		if progress != nil {
			progress(transport.UploadProgress{
				Path:          file.Path,
				UploadedFiles: uploadedFiles,
				TotalFiles:    totalFiles,
				UploadedBytes: uploadedBytes,
				TotalBytes:    totalBytes,
			})
		}
	}
	var (
		rawManifest []byte
		err         error
	)
	if strings.TrimSpace(release.ManifestPath) != "" {
		rawManifest, err = os.ReadFile(release.ManifestPath)
		if err != nil {
			return transport.UploadResult{}, err
		}
	} else {
		rawManifest, err = json.Marshal(release.Manifest)
		if err != nil {
			return transport.UploadResult{}, err
		}
	}
	f.files[path.Join(remotePath, "manifest.json")] = rawManifest
	if progress != nil {
		progress(transport.UploadProgress{
			Path:           "manifest.json",
			UploadedFiles:  totalFiles,
			TotalFiles:     totalFiles,
			UploadedBytes:  totalBytes,
			TotalBytes:     totalBytes,
			ManifestUpload: true,
		})
	}
	return transport.UploadResult{RemotePath: remotePath, ManifestPath: path.Join(remotePath, "manifest.json")}, nil
}

func (f *fakeTransport) uploadArchiveRelease(release build.Release, remotePath string, progress transport.UploadProgressFunc) (transport.UploadResult, error) {
	rawArchive, err := os.ReadFile(release.ArchivePath)
	if err != nil {
		return transport.UploadResult{}, err
	}
	var (
		rawManifest []byte
	)
	if strings.TrimSpace(release.ManifestPath) != "" {
		rawManifest, err = os.ReadFile(release.ManifestPath)
		if err != nil {
			return transport.UploadResult{}, err
		}
	} else {
		rawManifest, err = json.Marshal(release.Manifest)
		if err != nil {
			return transport.UploadResult{}, err
		}
	}
	totalBytes := int64(len(rawArchive) + len(rawManifest))
	f.files[path.Join(remotePath, "release.zip")] = rawArchive
	if progress != nil {
		progress(transport.UploadProgress{
			Path:          "release.zip",
			UploadedFiles: 1,
			TotalFiles:    2,
			UploadedBytes: int64(len(rawArchive)),
			TotalBytes:    totalBytes,
		})
	}
	f.files[path.Join(remotePath, "manifest.json")] = rawManifest
	if progress != nil {
		progress(transport.UploadProgress{
			Path:           "manifest.json",
			UploadedFiles:  2,
			TotalFiles:     2,
			UploadedBytes:  totalBytes,
			TotalBytes:     totalBytes,
			ManifestUpload: true,
		})
	}
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
	if f.files == nil {
		f.files = map[string][]byte{}
	}
	prefix := remotePath
	if !strings.HasSuffix(prefix, "/") && !strings.HasSuffix(prefix, `\`) {
		prefix += "/"
	}
	for existingPath := range f.files {
		if strings.HasPrefix(existingPath, prefix) {
			return os.ErrExist
		}
	}
	return nil
}

func (f *fakeTransport) MkdirAll(ctx context.Context, remotePath string) error {
	_ = ctx
	_ = remotePath
	return nil
}

func (f *fakeTransport) RemoveAll(ctx context.Context, remotePath string) error {
	_ = ctx
	if f.removeErr != nil {
		return f.removeErr
	}
	delete(f.files, remotePath)
	prefix := remotePath
	if !strings.HasSuffix(prefix, "/") && !strings.HasSuffix(prefix, `\`) {
		prefix += "/"
	}
	for existingPath := range f.files {
		if strings.HasPrefix(existingPath, prefix) {
			delete(f.files, existingPath)
		}
	}
	return nil
}

func (f *fakeTransport) Exists(ctx context.Context, remotePath string) (bool, error) {
	_ = ctx
	if _, ok := f.files[remotePath]; ok {
		return true, nil
	}
	prefix := remotePath
	if !strings.HasSuffix(prefix, "/") && !strings.HasSuffix(prefix, `\`) {
		prefix += "/"
	}
	for path := range f.files {
		if strings.HasPrefix(path, prefix) {
			return true, nil
		}
	}
	return false, nil
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

func TestPushBuildsReleaseWhenReleaseIsOmitted(t *testing.T) {
	tempDir := t.TempDir()
	store := state.New(filepath.Join(tempDir, "state.json"))

	builder := &fakeBuilder{
		buildRelease: build.Release{
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
				BeforeBuild:    []config.HookSpec{{Name: "before-build"}},
				AfterBuild:     []config.HookSpec{{Name: "after-build"}},
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
	if len(hooksRunner.phases) < 2 || hooksRunner.phases[0] != "before_build" || hooksRunner.phases[1] != "after_build" {
		t.Fatalf("expected build hooks before push hooks, got %#v", hooksRunner.phases)
	}

	snapshot, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Builds) != 1 || snapshot.Builds[0].ReleaseID != "rel-2" {
		t.Fatalf("expected push to record built release, got %#v", snapshot.Builds)
	}
}

func TestAcquireRemoteLockReclaimsStaleLock(t *testing.T) {
	transportImpl := &fakeTransport{
		files: map[string][]byte{
			"/remote/app/.deploypier/locks/deploy.lock/owner.txt": []byte("release_id=old-rel\ncreated_at=2026-07-15T09:00:00Z\n"),
		},
	}
	service := &Service{
		cfg: config.Config{
			Remote: config.RemoteConfig{
				AppRoot: "/remote/app",
			},
		},
		transport: transportImpl,
		now:       func() time.Time { return time.Date(2026, 7, 15, 10, 5, 0, 0, time.UTC) },
	}

	unlock, err := service.acquireRemoteLock(context.Background(), "new-rel")
	if err != nil {
		t.Fatalf("acquire remote lock: %v", err)
	}
	defer unlock()

	raw, ok := transportImpl.files["/remote/app/.deploypier/locks/deploy.lock/owner.txt"]
	if !ok {
		t.Fatalf("expected renewed owner.txt after stale lock recovery")
	}
	content := string(raw)
	if !strings.Contains(content, "release_id=new-rel") {
		t.Fatalf("expected new release id in lock owner: %s", content)
	}
}

func TestAcquireRemoteLockKeepsFreshLockProtected(t *testing.T) {
	transportImpl := &fakeTransport{
		files: map[string][]byte{
			"/remote/app/.deploypier/locks/deploy.lock/owner.txt": []byte("release_id=active-rel\ncreated_at=2026-07-15T10:00:00Z\n"),
		},
	}
	service := &Service{
		cfg: config.Config{
			Remote: config.RemoteConfig{
				AppRoot: "/remote/app",
			},
		},
		transport: transportImpl,
		now:       func() time.Time { return time.Date(2026, 7, 15, 10, 5, 0, 0, time.UTC) },
	}

	if _, err := service.acquireRemoteLock(context.Background(), "new-rel"); err == nil {
		t.Fatalf("expected fresh lock to block deploy")
	}
}

func TestRecoverRemoteLockIgnoresMissingRemovalWhenLockAlreadyGone(t *testing.T) {
	transportImpl := &fakeTransport{
		removeErr: status.Wrap(status.KindInternal, "remove ftps dir", os.ErrNotExist),
	}
	service := &Service{
		cfg: config.Config{
			Remote: config.RemoteConfig{
				AppRoot: "/remote/app",
			},
		},
		transport: transportImpl,
		now:       func() time.Time { return time.Date(2026, 7, 15, 10, 5, 0, 0, time.UTC) },
	}

	if err := service.recoverRemoteLock(context.Background(), "/remote/app/.deploypier/locks/deploy.lock"); err != nil {
		t.Fatalf("expected missing removal to be ignored: %v", err)
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

func TestPushBypassModeRunsPostDeployDespiteBlockedMigrationPolicy(t *testing.T) {
	tempDir := t.TempDir()
	store := state.New(filepath.Join(tempDir, "state.json"))

	release := build.Release{
		ID:   "rel-bypass-1",
		Path: filepath.Join(tempDir, "releases", "rel-bypass-1"),
		Manifest: build.Manifest{
			ReleaseID: "rel-bypass-1",
		},
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
	if err := os.WriteFile(filepath.Join(projectRoot, "database", "migrations", "2026_07_15_000001_alter_widgets_table.php"), []byte(`<?php
use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;
return new class {
    public function up(): void
    {
        Schema::table('widgets', function (Blueprint $table) {
            $table->string('name')->nullable();
        });
    }
};`), 0o644); err != nil {
		t.Fatalf("write migration: %v", err)
	}

	writeGitStub(t, projectRoot, "database/migrations/2026_07_15_000001_alter_widgets_table.php\n")
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", projectRoot+string(os.PathListSeparator)+originalPath)

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	builder := &fakeBuilder{loadRelease: release}
	transportImpl := &fakeTransport{}
	activator := &fakeActivator{}
	hooksRunner := &fakeHooks{}
	service := &Service{
		cfg: config.Config{
			Project: config.ProjectConfig{Root: projectRoot, Name: "demo"},
			Transport: config.TransportConfig{
				Path: "/",
			},
			Remote: config.RemoteConfig{
				AppRoot: "/app",
				Layout:  "release-based",
			},
			PostDeploy: config.PostDeployConfig{
				Mode:    "bypass",
				HookURL: server.URL,
				KeyID:   "test-key",
				Secret:  "test-secret",
			},
		},
		builder:    builder,
		store:      store,
		transport:  transportImpl,
		activator:  activator,
		hooks:      hooksRunner,
		postDeploy: postdeploy.NewClient(),
		now:        func() time.Time { return time.Date(2026, 7, 15, 10, 5, 0, 0, time.UTC) },
	}

	result, err := service.Push(context.Background(), release.ID, false)
	if err != nil {
		t.Fatalf("push service: %v", err)
	}

	if result.Status != "success" {
		t.Fatalf("expected success, got %s", result.Status)
	}
	if requests != 1 {
		t.Fatalf("expected hook to run once, got %d", requests)
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, "\n"), "bypass") {
		t.Fatalf("expected bypass warning, got %#v", result.Warnings)
	}
}

func TestPushUsesRemoteOpsToPrepareDeltaRelease(t *testing.T) {
	tempDir := t.TempDir()
	store := state.New(filepath.Join(tempDir, "state.json"))
	projectRoot := filepath.Join(tempDir, "project")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	releasePath := filepath.Join(tempDir, "releases", "rel-new-1")
	bundlePath := filepath.Join(releasePath, "bundle")
	if err := os.MkdirAll(filepath.Join(bundlePath, "app"), 0o755); err != nil {
		t.Fatalf("mkdir bundle app: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bundlePath, "public", "build"), 0o755); err != nil {
		t.Fatalf("mkdir bundle public: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundlePath, "app", "Foo.php"), []byte("<?php echo 'same';"), 0o644); err != nil {
		t.Fatalf("write Foo.php: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundlePath, "public", "build", "app.js"), []byte("console.log('new build');"), 0o644); err != nil {
		t.Fatalf("write app.js: %v", err)
	}

	sameContent := []byte("<?php echo 'same';")
	oldBuild := []byte("console.log('old build');")
	newBuild := []byte("console.log('new build');")
	oldAsset := []byte("legacy")

	release := build.Release{
		ID:           "rel-new-1",
		Path:         releasePath,
		BundlePath:   bundlePath,
		ManifestPath: filepath.Join(releasePath, "manifest.json"),
		Manifest: build.Manifest{
			ReleaseID: "rel-new-1",
			SHA256:    "aggregate-new",
			Files: []build.ManifestFile{
				{Path: "app/Foo.php", Size: int64(len(sameContent)), SHA256: fileSHA256Hex(t, sameContent)},
				{Path: "public/build/app.js", Size: int64(len(newBuild)), SHA256: fileSHA256Hex(t, newBuild)},
			},
		},
	}
	rawManifest, err := json.Marshal(release.Manifest)
	if err != nil {
		t.Fatalf("marshal release manifest: %v", err)
	}
	if err := os.MkdirAll(release.Path, 0o755); err != nil {
		t.Fatalf("mkdir release path: %v", err)
	}
	if err := os.WriteFile(release.ManifestPath, rawManifest, 0o644); err != nil {
		t.Fatalf("write release manifest: %v", err)
	}
	if err := store.RecordBuild(context.Background(), state.BuildRecord{
		ReleaseID: release.ID,
		Path:      release.Path,
		BuiltAt:   "2026-07-16T10:00:00Z",
	}); err != nil {
		t.Fatalf("record build: %v", err)
	}

	currentManifest := build.Manifest{
		ReleaseID: "rel-old-1",
		SHA256:    "aggregate-old",
		Files: []build.ManifestFile{
			{Path: "app/Foo.php", Size: int64(len(sameContent)), SHA256: fileSHA256Hex(t, sameContent)},
			{Path: "public/build/app.js", Size: int64(len(oldBuild)), SHA256: fileSHA256Hex(t, oldBuild)},
			{Path: "public/build/old.js", Size: int64(len(oldAsset)), SHA256: fileSHA256Hex(t, oldAsset)},
		},
	}
	currentManifestRaw, err := json.Marshal(currentManifest)
	if err != nil {
		t.Fatalf("marshal current manifest: %v", err)
	}

	transportImpl := &fakeTransport{
		files: map[string][]byte{
			"/app/releases/rel-old-1/manifest.json":       currentManifestRaw,
			"/app/releases/rel-old-1/app/Foo.php":         sameContent,
			"/app/releases/rel-old-1/public/build/app.js": oldBuild,
			"/app/releases/rel-old-1/public/build/old.js": oldAsset,
		},
	}

	prepareRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload postdeploy.Payload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.Operation != postdeploy.OperationPrepareRelease {
			t.Fatalf("unexpected operation: %s", payload.Operation)
		}
		prepareRequests++

		sourcePrefix := "/app/releases/rel-old-1/"
		targetPrefix := "/app/releases/rel-new-1/"
		for path, content := range transportImpl.files {
			if strings.HasPrefix(path, sourcePrefix) {
				targetPath := targetPrefix + strings.TrimPrefix(path, sourcePrefix)
				transportImpl.files[targetPath] = append([]byte{}, content...)
			}
		}
		for _, removed := range payload.RemovePaths {
			delete(transportImpl.files, targetPrefix+removed)
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"status":"completed"}`))
	}))
	defer server.Close()

	service := &Service{
		cfg: config.Config{
			Project: config.ProjectConfig{Root: projectRoot, Name: "demo"},
			Transport: config.TransportConfig{
				Path: "/",
			},
			Remote: config.RemoteConfig{
				AppRoot: "/app",
				Layout:  "release-based",
			},
			PostDeploy: config.PostDeployConfig{
				Mode:      "skip",
				RemoteOps: "auto",
				HookURL:   server.URL,
				KeyID:     "test-key",
				Secret:    "test-secret",
			},
		},
		builder:    &fakeBuilder{loadRelease: release},
		store:      store,
		transport:  transportImpl,
		activator:  &fakeActivator{current: "rel-old-1"},
		hooks:      &fakeHooks{},
		postDeploy: postdeploy.NewClient(),
		now:        func() time.Time { return time.Date(2026, 7, 16, 10, 5, 0, 0, time.UTC) },
	}

	result, err := service.Push(context.Background(), release.ID, true)
	if err != nil {
		t.Fatalf("push with remote ops: %v", err)
	}

	if prepareRequests != 1 {
		t.Fatalf("expected one prepare request, got %d", prepareRequests)
	}
	if len(transportImpl.uploaded) != 1 || transportImpl.uploaded[0] != "rel-new-1" {
		t.Fatalf("unexpected uploaded releases: %#v", transportImpl.uploaded)
	}
	if string(transportImpl.files["/app/releases/rel-new-1/app/Foo.php"]) != string(sameContent) {
		t.Fatalf("expected unchanged file to come from remote clone")
	}
	if string(transportImpl.files["/app/releases/rel-new-1/public/build/app.js"]) != string(newBuild) {
		t.Fatalf("expected changed file to be uploaded")
	}
	if _, ok := transportImpl.files["/app/releases/rel-new-1/public/build/old.js"]; ok {
		t.Fatalf("expected removed file to be pruned from prepared release")
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "remote ops prepared") {
		t.Fatalf("expected remote ops warning/summary, got %#v", result.Warnings)
	}
}

func TestPushArchiveModeUploadsZipAndExtractsRemotely(t *testing.T) {
	tempDir := t.TempDir()
	store := state.New(filepath.Join(tempDir, "state.json"))
	projectRoot := filepath.Join(tempDir, "project")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}

	releasePath := filepath.Join(tempDir, "releases", "rel-archive-1")
	bundlePath := filepath.Join(releasePath, "bundle")
	if err := os.MkdirAll(filepath.Join(bundlePath, "bootstrap"), 0o755); err != nil {
		t.Fatalf("mkdir bundle bootstrap: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(bundlePath, "public", "build"), 0o755); err != nil {
		t.Fatalf("mkdir bundle public: %v", err)
	}
	bootstrapContent := []byte("<?php return [];")
	jsContent := []byte("console.log('archive');")
	if err := os.WriteFile(filepath.Join(bundlePath, "bootstrap", "app.php"), bootstrapContent, 0o644); err != nil {
		t.Fatalf("write bootstrap/app.php: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bundlePath, "public", "build", "app.js"), jsContent, 0o644); err != nil {
		t.Fatalf("write public/build/app.js: %v", err)
	}

	release := build.Release{
		ID:           "rel-archive-1",
		Path:         releasePath,
		BundlePath:   bundlePath,
		ManifestPath: filepath.Join(releasePath, "manifest.json"),
		Manifest: build.Manifest{
			ReleaseID: "rel-archive-1",
			SHA256:    "archive-aggregate",
			Files: []build.ManifestFile{
				{Path: "bootstrap/app.php", Size: int64(len(bootstrapContent)), SHA256: fileSHA256Hex(t, bootstrapContent)},
				{Path: "public/build/app.js", Size: int64(len(jsContent)), SHA256: fileSHA256Hex(t, jsContent)},
			},
		},
	}
	if err := os.MkdirAll(release.Path, 0o755); err != nil {
		t.Fatalf("mkdir release path: %v", err)
	}
	rawManifest, err := json.Marshal(release.Manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	if err := os.WriteFile(release.ManifestPath, rawManifest, 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := store.RecordBuild(context.Background(), state.BuildRecord{
		ReleaseID: release.ID,
		Path:      release.Path,
		BuiltAt:   "2026-07-16T11:00:00Z",
	}); err != nil {
		t.Fatalf("record build: %v", err)
	}

	transportImpl := &fakeTransport{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload postdeploy.Payload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload.Operation != postdeploy.OperationExtractRelease {
			t.Fatalf("unexpected operation: %s", payload.Operation)
		}

		archive := transportImpl.files["/app/releases/rel-archive-1/release.zip"]
		extractArchiveIntoFiles(t, archive, "/app/releases/rel-archive-1", transportImpl.files)
		delete(transportImpl.files, "/app/releases/rel-archive-1/release.zip")

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true,"status":"completed"}`))
	}))
	defer server.Close()

	service := &Service{
		cfg: config.Config{
			Project: config.ProjectConfig{Root: projectRoot, Name: "demo"},
			Release: config.ReleaseConfig{
				UploadMode: "archive",
			},
			Transport: config.TransportConfig{
				Path: "/",
			},
			Remote: config.RemoteConfig{
				AppRoot: "/app",
				Layout:  "release-based",
			},
			PostDeploy: config.PostDeployConfig{
				Mode:      "skip",
				RemoteOps: "off",
				HookURL:   server.URL,
				KeyID:     "test-key",
				Secret:    "test-secret",
			},
		},
		builder:    &fakeBuilder{loadRelease: release},
		store:      store,
		transport:  transportImpl,
		activator:  &fakeActivator{},
		hooks:      &fakeHooks{},
		postDeploy: postdeploy.NewClient(),
		now:        func() time.Time { return time.Date(2026, 7, 16, 11, 5, 0, 0, time.UTC) },
	}

	result, err := service.Push(context.Background(), release.ID, true)
	if err != nil {
		t.Fatalf("push archive service: %v", err)
	}

	if result.RemotePath != "/app/releases/rel-archive-1" {
		t.Fatalf("unexpected remote path: %s", result.RemotePath)
	}
	if _, ok := transportImpl.files["/app/releases/rel-archive-1/release.zip"]; ok {
		t.Fatalf("expected archive to be removed after extraction")
	}
	if string(transportImpl.files["/app/releases/rel-archive-1/bootstrap/app.php"]) != string(bootstrapContent) {
		t.Fatalf("expected extracted bootstrap/app.php")
	}
	if string(transportImpl.files["/app/releases/rel-archive-1/public/build/app.js"]) != string(jsContent) {
		t.Fatalf("expected extracted public/build/app.js")
	}
}

func TestPushSeedsSharedRemoteEnvFromLocalEnvProduction(t *testing.T) {
	tempDir := t.TempDir()
	store := state.New(filepath.Join(tempDir, "state.json"))

	release := build.Release{
		ID:         "rel-env-1",
		Path:       filepath.Join(tempDir, "releases", "rel-env-1"),
		BundlePath: filepath.Join(tempDir, "bundle"),
		Manifest: build.Manifest{
			ReleaseID: "rel-env-1",
		},
	}
	if err := store.RecordBuild(context.Background(), state.BuildRecord{
		ReleaseID: release.ID,
		Path:      release.Path,
		BuiltAt:   "2026-07-15T10:00:00Z",
	}); err != nil {
		t.Fatalf("record build: %v", err)
	}

	projectRoot := filepath.Join(tempDir, "project")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "env.production"), []byte("APP_ENV=production\nAPP_KEY=base64:test\n"), 0o644); err != nil {
		t.Fatalf("write env.production: %v", err)
	}

	service := &Service{
		cfg: config.Config{
			Project: config.ProjectConfig{Root: projectRoot},
			Transport: config.TransportConfig{
				Path: "/",
			},
			Remote: config.RemoteConfig{
				AppRoot: "/app",
			},
			PostDeploy: config.PostDeployConfig{
				Mode: "skip",
			},
		},
		builder:   &fakeBuilder{loadRelease: release},
		store:     store,
		transport: &fakeTransport{},
		activator: &fakeActivator{},
		hooks:     &fakeHooks{},
		now:       func() time.Time { return time.Date(2026, 7, 15, 10, 5, 0, 0, time.UTC) },
	}

	result, err := service.Push(context.Background(), release.ID, true)
	if err != nil {
		t.Fatalf("push service: %v", err)
	}
	if result.RemotePath != "/app/releases/rel-env-1" {
		t.Fatalf("unexpected remote path: %s", result.RemotePath)
	}

	transportImpl := service.transport.(*fakeTransport)
	sharedEnv, ok := transportImpl.files["/.env"]
	if !ok {
		t.Fatalf("expected shared remote .env to be created")
	}
	releaseEnv, ok := transportImpl.files["/app/releases/rel-env-1/.env"]
	if !ok {
		t.Fatalf("expected release .env to be created")
	}
	if string(sharedEnv) != "APP_ENV=production\nAPP_KEY=base64:test\n" {
		t.Fatalf("unexpected shared env content: %s", string(sharedEnv))
	}
	if string(releaseEnv) != string(sharedEnv) {
		t.Fatalf("expected release env to match shared env")
	}
}

func TestPushReusesExistingSharedRemoteEnv(t *testing.T) {
	tempDir := t.TempDir()
	store := state.New(filepath.Join(tempDir, "state.json"))

	release := build.Release{
		ID:         "rel-env-2",
		Path:       filepath.Join(tempDir, "releases", "rel-env-2"),
		BundlePath: filepath.Join(tempDir, "bundle"),
		Manifest: build.Manifest{
			ReleaseID: "rel-env-2",
		},
	}
	if err := store.RecordBuild(context.Background(), state.BuildRecord{
		ReleaseID: release.ID,
		Path:      release.Path,
		BuiltAt:   "2026-07-15T10:00:00Z",
	}); err != nil {
		t.Fatalf("record build: %v", err)
	}

	projectRoot := filepath.Join(tempDir, "project")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectRoot, "env.production"), []byte("LOCAL_ONLY=true\n"), 0o644); err != nil {
		t.Fatalf("write env.production: %v", err)
	}

	transportImpl := &fakeTransport{
		files: map[string][]byte{
			"/.env": []byte("APP_ENV=staging\nAPP_KEY=base64:remote\n"),
		},
	}
	service := &Service{
		cfg: config.Config{
			Project: config.ProjectConfig{Root: projectRoot},
			Transport: config.TransportConfig{
				Path: "/",
			},
			Remote: config.RemoteConfig{
				AppRoot: "/app",
			},
			PostDeploy: config.PostDeployConfig{
				Mode: "skip",
			},
		},
		builder:   &fakeBuilder{loadRelease: release},
		store:     store,
		transport: transportImpl,
		activator: &fakeActivator{},
		hooks:     &fakeHooks{},
		now:       func() time.Time { return time.Date(2026, 7, 15, 10, 5, 0, 0, time.UTC) },
	}

	if _, err := service.Push(context.Background(), release.ID, true); err != nil {
		t.Fatalf("push service: %v", err)
	}

	if string(transportImpl.files["/.env"]) != "APP_ENV=staging\nAPP_KEY=base64:remote\n" {
		t.Fatalf("expected remote shared env to be preserved")
	}
	if string(transportImpl.files["/app/releases/rel-env-2/.env"]) != "APP_ENV=staging\nAPP_KEY=base64:remote\n" {
		t.Fatalf("expected release env to reuse remote shared env")
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

func TestDoctorCreatesBootstrapPublicIndexWhenMissing(t *testing.T) {
	transportImpl := &fakeTransport{}
	service := &Service{
		cfg: config.Config{
			Remote: config.RemoteConfig{
				PublicRoot: "/remote/public_html",
				Layout:     "release-based",
			},
			Runtime: config.RuntimeConfig{
				AppRoot:        "/home/storage/demo/app",
				CurrentPointer: "/home/storage/demo/.deploypier/current.txt",
			},
			Activation: config.ActivationConfig{
				Kind:           "pointer",
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
	if check.Report.Level != status.LevelOK {
		t.Fatalf("expected ok after bootstrap creation, got %s", check.Report.Level)
	}
	if check.Report.Code != "created" {
		t.Fatalf("expected created code, got %s", check.Report.Code)
	}

	indexPath := "/remote/public_html/index.php"
	raw, ok := transportImpl.files[indexPath]
	if !ok {
		t.Fatalf("expected bootstrap public index to be written")
	}
	content := string(raw)
	if !strings.Contains(content, "/home/storage/demo/app") {
		t.Fatalf("expected runtime app path in bootstrap index: %s", content)
	}
	if !strings.Contains(content, "/home/storage/demo/.deploypier/current.txt") {
		t.Fatalf("expected runtime pointer path in bootstrap index: %s", content)
	}
	if !strings.Contains(content, "$app->usePublicPath(__DIR__);") {
		t.Fatalf("expected usePublicPath in bootstrap index: %s", content)
	}
}

func TestInspectRemoteSuggestsBasePathFromCurrentDir(t *testing.T) {
	transportImpl := &fakeTransport{
		inspect: transport.Inspection{
			CurrentDir:   "/home/storage/b/ef/25/demo",
			ResolvedPath: "/home/demo",
		},
		files: map[string][]byte{
			"/home/storage/b/ef/25/demo/public_html/index.php": []byte("<?php"),
		},
	}
	service := &Service{
		cfg: config.Config{
			Transport: config.TransportConfig{
				Path: "/home/demo",
			},
		},
		transport: transportImpl,
	}

	inspection, err := service.InspectRemote(context.Background())
	if err != nil {
		t.Fatalf("inspect remote: %v", err)
	}

	if inspection.SuggestedBasePath != "/home/storage/b/ef/25/demo" {
		t.Fatalf("unexpected suggested base path: %s", inspection.SuggestedBasePath)
	}
	if inspection.SuggestedPublicRoot != "/home/storage/b/ef/25/demo/public_html" {
		t.Fatalf("unexpected suggested public root: %s", inspection.SuggestedPublicRoot)
	}
	if !inspection.PublicHTMLExists {
		t.Fatalf("expected public_html to be detected")
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

func fileSHA256Hex(t *testing.T, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func extractArchiveIntoFiles(t *testing.T, archive []byte, targetPrefix string, files map[string][]byte) {
	t.Helper()

	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("open test archive: %v", err)
	}

	for _, entry := range reader.File {
		handle, err := entry.Open()
		if err != nil {
			t.Fatalf("open archive entry: %v", err)
		}
		content, err := io.ReadAll(handle)
		_ = handle.Close()
		if err != nil {
			t.Fatalf("read archive entry: %v", err)
		}
		files[path.Join(targetPrefix, filepath.ToSlash(entry.Name))] = content
	}
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
