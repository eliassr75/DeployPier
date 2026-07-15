package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/deploypier/deploypier/internal/activation"
	"github.com/deploypier/deploypier/internal/build"
	"github.com/deploypier/deploypier/internal/config"
	"github.com/deploypier/deploypier/internal/hooks"
	"github.com/deploypier/deploypier/internal/migrations"
	"github.com/deploypier/deploypier/internal/postdeploy"
	"github.com/deploypier/deploypier/internal/state"
	"github.com/deploypier/deploypier/internal/status"
	"github.com/deploypier/deploypier/internal/transport"
)

type Builder interface {
	Build(ctx context.Context, cfg config.Config) (build.Release, error)
	Load(ctx context.Context, cfg config.Config, releaseID string) (build.Release, error)
}

type Service struct {
	cfg        config.Config
	builder    Builder
	store      *state.Store
	transport  transport.Transport
	activator  activation.Activator
	hooks      hooks.Runner
	postDeploy *postdeploy.Client
	now        func() time.Time
}

type Plan struct {
	Project        string
	SourceRoot     string
	ReleaseDir     string
	Transport      string
	Activation     string
	LatestBuild    string
	CurrentRelease string
	Steps          []string
	Layout         string
	PostDeployMode string
}

type DoctorCheck struct {
	Name    string
	Report  status.Report
	Details string
}

type PushResult struct {
	ReleaseID      string
	RemotePath     string
	ActivatedPath  string
	Activated      bool
	HookSummaries  []hooks.Result
	ActivationInfo string
	Status         string
	Warnings       []string
}

func New(cfg config.Config) (*Service, error) {
	builder := build.NewBuilder()
	store := state.New(cfg.State.File)
	transportImpl, err := transport.New(cfg.Transport)
	if err != nil {
		return nil, err
	}
	activatorImpl, err := activation.New(cfg.Activation, cfg.Remote, transportImpl)
	if err != nil {
		return nil, err
	}
	return &Service{
		cfg:        cfg,
		builder:    builder,
		store:      store,
		transport:  transportImpl,
		activator:  activatorImpl,
		hooks:      hooks.NewRunner(),
		postDeploy: postdeploy.NewClient(),
		now:        time.Now,
	}, nil
}

func (s *Service) Plan(ctx context.Context) (Plan, error) {
	snapshot, err := s.store.Snapshot(ctx)
	if err != nil {
		return Plan{}, status.Wrap(status.KindInternal, "load state snapshot", err)
	}

	latestBuild, _ := state.LatestBuild(snapshot)
	currentRelease, _ := s.activator.Current(ctx)

	return Plan{
		Project:        s.cfg.Project.Name,
		SourceRoot:     s.cfg.Project.Root,
		ReleaseDir:     s.cfg.Release.Directory,
		Transport:      s.transport.Name(),
		Activation:     s.activator.Name(),
		LatestBuild:    latestBuild.ReleaseID,
		CurrentRelease: currentRelease,
		Layout:         s.cfg.Remote.Layout,
		PostDeployMode: s.cfg.PostDeploy.Mode,
		Steps: []string{
			"doctor validates config, hooks, and local transport health",
			"build creates a release directory with bundle files and manifest.json",
			"push uploads a built release, updates activation, and can call the Laravel post-deploy hook",
			"rollback points activation back to the previous recorded release",
		},
	}, nil
}

func (s *Service) Doctor(ctx context.Context) ([]DoctorCheck, error) {
	checks := []DoctorCheck{
		{
			Name:    "config",
			Report:  status.Report{Level: status.LevelOK, Code: "ok", Message: "config loaded"},
			Details: s.cfg.Project.Name,
		},
		{
			Name:    "source_root",
			Report:  status.Classify(statPath(s.cfg.Project.Root)),
			Details: s.cfg.Project.Root,
		},
		{
			Name:    "release_parent",
			Report:  status.Classify(statPath(filepath.Dir(s.cfg.Release.Directory))),
			Details: filepath.Dir(s.cfg.Release.Directory),
		},
		{
			Name:    "transport",
			Report:  s.transport.Probe(ctx),
			Details: s.transport.Name(),
		},
		{
			Name:    "layout",
			Report:  status.Report{Level: status.LevelOK, Code: "ok", Message: "remote layout selected"},
			Details: s.cfg.Remote.Layout,
		},
	}

	current, err := s.activator.Current(ctx)
	checks = append(checks, DoctorCheck{
		Name:    "activation",
		Report:  status.Classify(err),
		Details: current,
	})

	hookCount := len(s.cfg.Hooks.BeforeBuild) +
		len(s.cfg.Hooks.AfterBuild) +
		len(s.cfg.Hooks.BeforePush) +
		len(s.cfg.Hooks.AfterPush) +
		len(s.cfg.Hooks.BeforeActivate) +
		len(s.cfg.Hooks.AfterActivate) +
		len(s.cfg.Hooks.BeforeRollback) +
		len(s.cfg.Hooks.AfterRollback)
	checks = append(checks, DoctorCheck{
		Name:    "hooks",
		Report:  status.Report{Level: status.LevelOK, Code: "ok", Message: "hooks parsed"},
		Details: fmt.Sprintf("%d configured", hookCount),
	})
	checks = append(checks, DoctorCheck{
		Name:    "post_deploy",
		Report:  s.postDeployReport(),
		Details: s.cfg.PostDeploy.Mode,
	})

	return checks, nil
}

func (s *Service) Build(ctx context.Context) (build.Release, error) {
	if _, err := s.runHooks(ctx, "before_build", hooks.Metadata{
		"project": s.cfg.Project.Name,
	}); err != nil {
		return build.Release{}, err
	}

	release, err := s.builder.Build(ctx, s.cfg)
	if err != nil {
		return build.Release{}, err
	}

	if err := s.store.RecordBuild(ctx, state.BuildRecord{
		ReleaseID: release.ID,
		Path:      release.Path,
		BuiltAt:   s.now().UTC().Format(time.RFC3339),
	}); err != nil {
		return build.Release{}, status.Wrap(status.KindInternal, "record build", err)
	}

	if _, err := s.runHooks(ctx, "after_build", hooks.Metadata{
		"project":    s.cfg.Project.Name,
		"release_id": release.ID,
		"release":    release.Path,
	}); err != nil {
		return build.Release{}, err
	}

	return release, nil
}

func (s *Service) Push(ctx context.Context, releaseID string, skipActivate bool) (PushResult, error) {
	assessment, err := migrations.Assess(ctx, s.cfg.Project.Root)
	if err != nil {
		return PushResult{}, err
	}

	if s.cfg.PostDeploy.Mode == "auto" && !assessment.AutoAllowed {
		return PushResult{
			Status:   "needs_manual_migration",
			Warnings: append([]string{}, assessment.Blocking...),
		}, status.Wrap(status.KindConflict, "assess migrations", fmt.Errorf("automatic post-deploy blocked by risky migrations"))
	}

	release, err := s.resolveRelease(ctx, releaseID)
	if err != nil {
		return PushResult{}, err
	}

	unlock, err := s.acquireRemoteLock(ctx, release.ID)
	if err != nil {
		return PushResult{}, err
	}
	defer unlock()

	result := PushResult{
		ReleaseID: release.ID,
		Status:    "success",
		Warnings:  append([]string{}, assessment.Warnings...),
	}
	beforePush, err := s.runHooks(ctx, "before_push", hooks.Metadata{
		"release_id": release.ID,
		"release":    release.Path,
	})
	result.HookSummaries = append(result.HookSummaries, beforePush...)
	if err != nil {
		return result, err
	}

	remoteReleasePath := s.remoteReleasePath(release.ID)
	upload, err := s.transport.UploadRelease(ctx, release, remoteReleasePath)
	if err != nil {
		return result, err
	}
	if err := s.validateRemoteRelease(ctx, release, upload.RemotePath); err != nil {
		return result, err
	}
	result.RemotePath = upload.RemotePath

	if err := s.store.RecordPush(ctx, state.PushRecord{
		ReleaseID:  release.ID,
		RemotePath: upload.RemotePath,
		PushedAt:   s.now().UTC().Format(time.RFC3339),
	}); err != nil {
		return result, status.Wrap(status.KindInternal, "record push", err)
	}

	afterPush, err := s.runHooks(ctx, "after_push", hooks.Metadata{
		"release_id": release.ID,
		"remote":     upload.RemotePath,
	})
	result.HookSummaries = append(result.HookSummaries, afterPush...)
	if err != nil {
		return result, err
	}

	if skipActivate {
		_ = s.pruneRemoteReleases(ctx)
		return result, nil
	}

	beforeActivate, err := s.runHooks(ctx, "before_activate", hooks.Metadata{
		"release_id": release.ID,
		"remote":     upload.RemotePath,
	})
	result.HookSummaries = append(result.HookSummaries, beforeActivate...)
	if err != nil {
		return result, err
	}

	activated, err := s.activator.Activate(ctx, release.ID, "push")
	if err != nil {
		return result, err
	}
	result.Activated = true
	result.ActivatedPath = activated.PublicPath
	result.ActivationInfo = activated.Message
	if activated.Degraded {
		result.Status = "degraded_success"
	}

	if err := s.store.RecordActivation(ctx, state.ActivationRecord{
		ReleaseID:   release.ID,
		ActivatedAt: s.now().UTC().Format(time.RFC3339),
		Reason:      "push",
	}); err != nil {
		return result, status.Wrap(status.KindInternal, "record activation", err)
	}

	afterActivate, err := s.runHooks(ctx, "after_activate", hooks.Metadata{
		"release_id": release.ID,
		"remote":     upload.RemotePath,
		"active":     activated.PublicPath,
	})
	result.HookSummaries = append(result.HookSummaries, afterActivate...)
	if err != nil {
		return result, err
	}

	if s.cfg.PostDeploy.Mode == "manual" && assessment.HasMigrations {
		result.Status = "needs_manual_migration"
		if len(assessment.Blocking) > 0 {
			result.Warnings = append(result.Warnings, assessment.Blocking...)
		} else {
			result.Warnings = append(result.Warnings, "manual migration required for detected migration files")
		}
		return result, nil
	}

	if s.cfg.PostDeploy.Mode == "skip" && assessment.HasMigrations {
		result.Warnings = append(result.Warnings, "migration files detected but post-deploy mode is skip")
	}

	if s.cfg.PostDeploy.Mode == "auto" {
		ref, commit := currentGitMetadata(ctx, s.cfg.Project.Root)
		if _, err := s.postDeploy.Call(ctx, s.cfg, release, upload.RemotePath, ref, commit); err != nil {
			result.Status = "failed_post_deploy"
			result.Warnings = append(result.Warnings, err.Error())
			return result, err
		}
	}

	_ = s.pruneRemoteReleases(ctx)

	return result, nil
}

func (s *Service) Rollback(ctx context.Context, targetRelease string) (string, error) {
	target := targetRelease
	if target == "" {
		var err error
		target, err = s.activator.Previous(ctx)
		if err != nil {
			return "", err
		}
	}

	unlock, err := s.acquireRemoteLock(ctx, target)
	if err != nil {
		return "", err
	}
	defer unlock()

	if _, err := s.runHooks(ctx, "before_rollback", hooks.Metadata{
		"release_id": target,
	}); err != nil {
		return "", err
	}

	activated, err := s.activator.Activate(ctx, target, "rollback")
	if err != nil {
		return "", err
	}

	if err := s.store.RecordActivation(ctx, state.ActivationRecord{
		ReleaseID:   target,
		ActivatedAt: s.now().UTC().Format(time.RFC3339),
		Reason:      "rollback",
	}); err != nil {
		return "", status.Wrap(status.KindInternal, "record rollback activation", err)
	}

	if _, err := s.runHooks(ctx, "after_rollback", hooks.Metadata{
		"release_id": target,
		"active":     activated.PublicPath,
	}); err != nil {
		return "", err
	}

	return activated.PublicPath, nil
}

func (s *Service) runHooks(ctx context.Context, phase string, metadata hooks.Metadata) ([]hooks.Result, error) {
	return s.hooks.RunPhase(ctx, phase, s.cfg.Hooks.ForPhase(phase), metadata)
}

func (s *Service) resolveRelease(ctx context.Context, releaseID string) (build.Release, error) {
	target := releaseID
	if target == "" {
		snapshot, err := s.store.Snapshot(ctx)
		if err != nil {
			return build.Release{}, status.Wrap(status.KindInternal, "load state snapshot", err)
		}
		record, ok := state.LatestBuild(snapshot)
		if !ok {
			return build.Release{}, status.Wrap(status.KindNotFound, "select release", os.ErrNotExist)
		}
		target = record.ReleaseID
	}
	return s.builder.Load(ctx, s.cfg, target)
}

func statPath(path string) error {
	_, err := os.Stat(path)
	return err
}

func (s *Service) postDeployReport() status.Report {
	if s.cfg.PostDeploy.Mode == "skip" {
		return status.Report{Level: status.LevelWarn, Code: "disabled", Message: "post-deploy hook skipped"}
	}
	if s.cfg.PostDeploy.Mode == "manual" {
		return status.Report{Level: status.LevelWarn, Code: "manual", Message: "post-deploy hook is manual and will not run automatically"}
	}
	if strings.TrimSpace(s.cfg.PostDeploy.HookURL) == "" {
		return status.Report{Level: status.LevelFail, Code: "missing_hook_url", Message: "post-deploy hook URL env is unresolved"}
	}
	if strings.TrimSpace(s.cfg.PostDeploy.KeyID) == "" || strings.TrimSpace(s.cfg.PostDeploy.Secret) == "" {
		return status.Report{Level: status.LevelFail, Code: "missing_credentials", Message: "post-deploy credentials are unresolved"}
	}
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(s.cfg.PostDeploy.HookURL)), "https://") {
		return status.Report{Level: status.LevelFail, Code: "invalid_hook_url", Message: "post-deploy hook URL must use https"}
	}
	return status.Report{Level: status.LevelOK, Code: "ok", Message: "post-deploy hook configured"}
}

func (s *Service) remoteReleasePath(releaseID string) string {
	return path.Join(s.cfg.Remote.AppRoot, "releases", releaseID)
}

func (s *Service) remoteLockPath() string {
	return path.Join(s.cfg.Remote.AppRoot, ".deploypier", "locks", "deploy.lock")
}

func (s *Service) acquireRemoteLock(ctx context.Context, releaseID string) (func(), error) {
	lockPath := s.remoteLockPath()
	if err := s.transport.MkdirAll(ctx, path.Dir(lockPath)); err != nil {
		return nil, err
	}
	if err := s.transport.Mkdir(ctx, lockPath); err != nil {
		return nil, status.Wrap(status.KindConflict, "acquire remote deploy lock", err)
	}
	lockData := fmt.Sprintf("release_id=%s\ncreated_at=%s\n", releaseID, s.now().UTC().Format(time.RFC3339))
	if err := s.transport.WriteFile(ctx, path.Join(lockPath, "owner.txt"), []byte(lockData)); err != nil {
		_ = s.transport.RemoveAll(ctx, lockPath)
		return nil, err
	}
	return func() {
		_ = s.transport.RemoveAll(context.Background(), lockPath)
	}, nil
}

func (s *Service) validateRemoteRelease(ctx context.Context, release build.Release, remotePath string) error {
	raw, err := s.transport.ReadFile(ctx, path.Join(remotePath, "manifest.json"))
	if err != nil {
		return status.Wrap(status.KindConflict, "read remote manifest", err)
	}

	var remoteManifest build.Manifest
	if err := json.Unmarshal(raw, &remoteManifest); err != nil {
		return status.Wrap(status.KindConflict, "decode remote manifest", err)
	}
	if remoteManifest.ReleaseID != release.Manifest.ReleaseID {
		return status.Wrap(status.KindConflict, "validate remote manifest", fmt.Errorf("remote manifest release id mismatch"))
	}
	if remoteManifest.SHA256 != release.Manifest.SHA256 {
		return status.Wrap(status.KindConflict, "validate remote manifest", fmt.Errorf("remote manifest aggregate hash mismatch"))
	}
	if len(remoteManifest.Files) != len(release.Manifest.Files) {
		return status.Wrap(status.KindConflict, "validate remote manifest", fmt.Errorf("remote manifest file count mismatch"))
	}

	for _, file := range release.Manifest.Files {
		info, err := s.transport.HashFile(ctx, path.Join(remotePath, file.Path))
		if err != nil {
			return status.Wrap(status.KindConflict, "hash remote release file", err)
		}
		if info.Size != file.Size || info.SHA256 != file.SHA256 {
			return status.Wrap(status.KindConflict, "validate remote release file", fmt.Errorf("remote file mismatch: %s", file.Path))
		}
	}

	return nil
}

func (s *Service) pruneRemoteReleases(ctx context.Context) error {
	releasesRoot := path.Join(s.cfg.Remote.AppRoot, "releases")
	entries, err := s.transport.ReadDir(ctx, releasesRoot)
	if err != nil {
		return nil
	}

	current, _ := s.activator.Current(ctx)
	previous, _ := s.activator.Previous(ctx)
	keep := map[string]struct{}{}
	if current != "" {
		keep[current] = struct{}{}
	}
	if previous != "" {
		keep[previous] = struct{}{}
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir {
			names = append(names, path.Base(entry.Path))
		}
	}
	sort.Strings(names)

	candidates := make([]string, 0, len(names))
	for _, name := range names {
		if _, ok := keep[name]; ok {
			continue
		}
		candidates = append(candidates, name)
	}
	if len(candidates) <= s.cfg.Release.Retain {
		return nil
	}

	for _, name := range candidates[:len(candidates)-s.cfg.Release.Retain] {
		if err := s.transport.RemoveAll(ctx, path.Join(releasesRoot, name)); err != nil {
			return err
		}
	}
	return nil
}

func currentGitMetadata(ctx context.Context, dir string) (string, string) {
	return gitCommand(ctx, dir, "git rev-parse --abbrev-ref HEAD"), gitCommand(ctx, dir, "git rev-parse HEAD")
}

func gitCommand(ctx context.Context, dir string, command string) string {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-lc", command)
	}
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}
