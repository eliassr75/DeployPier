package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
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

type RemoteInspection struct {
	Transport                string
	CurrentDir               string
	ConfiguredTransportPath  string
	ResolvedTransportPath    string
	ConfiguredRuntimeAppRoot string
	ConfiguredRuntimePointer string
	SuggestedTransportPath   string
	SuggestedAppRoot         string
	SuggestedPublicRoot      string
	SuggestedCurrentPointer  string
	SuggestedRuntimeAppRoot  string
	SuggestedRuntimePointer  string
	SuggestedBasePath        string
	BasePathSource           string
	PublicHTMLExists         bool
	AppDirExists             bool
	Notes                    []string
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
			"push uploads a built release, synchronizes public assets, updates the current release pointer, and can call the Laravel post-deploy hook",
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
	checks = append(checks, s.publicIndexCheck(ctx))

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

func (s *Service) InspectRemote(ctx context.Context) (RemoteInspection, error) {
	inspection, err := s.transport.Inspect(ctx)
	if err != nil {
		return RemoteInspection{}, err
	}

	result := RemoteInspection{
		Transport:                s.transport.Name(),
		CurrentDir:               inspection.CurrentDir,
		ConfiguredTransportPath:  s.cfg.Transport.Path,
		ResolvedTransportPath:    inspection.ResolvedPath,
		ConfiguredRuntimeAppRoot: s.cfg.Runtime.AppRoot,
		ConfiguredRuntimePointer: s.cfg.Runtime.CurrentPointer,
	}

	candidates := uniqueNonEmpty(
		canonicalRemoteCandidate(inspection.ResolvedPath),
		canonicalRemoteCandidate(inspection.CurrentDir),
		canonicalRemoteCandidate(s.cfg.Transport.Path),
	)

	bestBase := ""
	bestSource := ""
	bestScore := -1
	bestPublic := false
	bestApp := false

	for _, candidate := range candidates {
		score := 0
		publicExists, _ := s.transport.Exists(ctx, joinServicePath(candidate, "public_html"))
		appExists, _ := s.transport.Exists(ctx, joinServicePath(candidate, "app"))

		if publicExists {
			score += 3
		}
		if appExists {
			score += 2
		}
		if candidate == canonicalRemoteCandidate(inspection.CurrentDir) {
			score++
		}

		if score > bestScore {
			bestScore = score
			bestBase = candidate
			bestPublic = publicExists
			bestApp = appExists
			switch candidate {
			case canonicalRemoteCandidate(inspection.ResolvedPath):
				bestSource = "resolved_transport_path"
			case canonicalRemoteCandidate(inspection.CurrentDir):
				bestSource = "current_dir"
			default:
				bestSource = "configured_transport_path"
			}
		}
	}

	if bestBase == "" {
		bestBase = canonicalRemoteCandidate(s.cfg.Transport.Path)
		bestSource = "configured_transport_path"
	}

	result.SuggestedBasePath = bestBase
	result.BasePathSource = bestSource
	result.PublicHTMLExists = bestPublic
	result.AppDirExists = bestApp
	result.SuggestedTransportPath = bestBase
	if bestBase != "" {
		result.SuggestedAppRoot = joinServicePath(bestBase, "app")
		result.SuggestedPublicRoot = joinServicePath(bestBase, "public_html")
		result.SuggestedCurrentPointer = joinServicePath(bestBase, ".deploypier", "current.txt")
	}
	if bestBase != "" && bestBase != "/" {
		result.SuggestedRuntimeAppRoot = joinServicePath(bestBase, "app")
		result.SuggestedRuntimePointer = joinServicePath(bestBase, ".deploypier", "current.txt")
	} else {
		result.Notes = append(result.Notes, "runtime php paths could not be inferred from the transport root alone; confirm the absolute account path before copying the public index example")
	}

	if !bestPublic {
		result.Notes = append(result.Notes, "public_html was not confirmed under the suggested base path; confirm the real remote root in the hosting panel or via temporary SSH if needed")
	}
	if !bestApp {
		result.Notes = append(result.Notes, "app directory was not found yet; this is expected before the first DeployPier push")
	}
	if strings.TrimSpace(inspection.CurrentDir) != "" && strings.TrimSpace(inspection.ResolvedPath) != "" && canonicalRemoteCandidate(inspection.CurrentDir) != canonicalRemoteCandidate(inspection.ResolvedPath) {
		result.Notes = append(result.Notes, "the transport current directory differs from transport.path; review the suggested base path before persisting it")
	}
	if strings.TrimSpace(inspection.CurrentDir) == "" {
		result.Notes = append(result.Notes, "remote current directory could not be determined; suggestions are based on the configured path only")
	}

	return result, nil
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
	if s.cfg.PostDeploy.Mode == "bypass" && len(assessment.Blocking) > 0 {
		result.Warnings = append(result.Warnings, "post-deploy bypass enabled; running hook despite migration policy blocks")
		result.Warnings = append(result.Warnings, assessment.Blocking...)
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
	if err := s.ensureReleaseEnvironment(ctx, upload.RemotePath); err != nil {
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

	if s.cfg.PostDeploy.Mode == "auto" || s.cfg.PostDeploy.Mode == "bypass" {
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
	if s.cfg.PostDeploy.Mode == "bypass" {
		return status.Report{Level: status.LevelWarn, Code: "bypass", Message: "post-deploy hook will run with migration policy bypass enabled"}
	}
	return status.Report{Level: status.LevelOK, Code: "ok", Message: "post-deploy hook configured"}
}

func (s *Service) publicIndexCheck(ctx context.Context) DoctorCheck {
	indexPath := joinServicePath(s.cfg.Remote.PublicRoot, "index.php")
	exists, err := s.transport.Exists(ctx, indexPath)
	if err != nil {
		return DoctorCheck{
			Name:    "public_index",
			Report:  status.Classify(err),
			Details: indexPath,
		}
	}
	if !exists {
		if s.shouldBootstrapPublicIndex() {
			if err := s.bootstrapPublicIndex(ctx, indexPath); err != nil {
				return DoctorCheck{
					Name:    "public_index",
					Report:  status.Classify(err),
					Details: indexPath,
				}
			}
			return DoctorCheck{
				Name:    "public_index",
				Report:  status.Report{Level: status.LevelOK, Code: "created", Message: "remote public index was missing and a DeployPier bootstrap index.php was created"},
				Details: indexPath,
			}
		}
		return DoctorCheck{
			Name:    "public_index",
			Report:  status.Report{Level: status.LevelWarn, Code: "missing", Message: "remote public index is missing; adapt it to DeployPier current pointer mode before the first activation"},
			Details: indexPath,
		}
	}

	raw, err := s.transport.ReadFile(ctx, indexPath)
	if err != nil {
		return DoctorCheck{
			Name:    "public_index",
			Report:  status.Classify(err),
			Details: indexPath,
		}
	}
	content := string(raw)
	missing := make([]string, 0, 3)
	for _, marker := range []string{
		serviceBase(s.cfg.Activation.CurrentPointer),
		"usePublicPath",
		"/releases/",
	} {
		if !strings.Contains(content, marker) {
			missing = append(missing, marker)
		}
	}
	if len(missing) > 0 {
		return DoctorCheck{
			Name:    "public_index",
			Report:  status.Report{Level: status.LevelWarn, Code: "needs_adaptation", Message: fmt.Sprintf("remote public index is not ready for current pointer mode; missing markers: %s", strings.Join(missing, ", "))},
			Details: indexPath,
		}
	}

	return DoctorCheck{
		Name:    "public_index",
		Report:  status.Report{Level: status.LevelOK, Code: "ok", Message: "remote public index is compatible with current pointer mode"},
		Details: indexPath,
	}
}

func (s *Service) shouldBootstrapPublicIndex() bool {
	return strings.EqualFold(strings.TrimSpace(s.cfg.Remote.Layout), "release-based") &&
		strings.EqualFold(strings.TrimSpace(s.cfg.Activation.Kind), "pointer") &&
		strings.TrimSpace(s.cfg.Runtime.AppRoot) != "" &&
		strings.TrimSpace(s.cfg.Runtime.CurrentPointer) != ""
}

func (s *Service) bootstrapPublicIndex(ctx context.Context, indexPath string) error {
	if err := s.transport.MkdirAll(ctx, serviceDir(indexPath)); err != nil {
		return status.Wrap(status.KindInternal, "prepare remote public index directory", err)
	}
	if err := s.transport.WriteFile(ctx, indexPath, []byte(s.renderBootstrapPublicIndex())); err != nil {
		return status.Wrap(status.KindInternal, "write remote public index", err)
	}
	return nil
}

func (s *Service) renderBootstrapPublicIndex() string {
	return fmt.Sprintf(`<?php
declare(strict_types=1);

// Generated by DeployPier doctor for current-pointer activation.
// Review the runtime paths below if the hosting account uses a different absolute PHP path.
$basePath = '%s';
$pointerFile = '%s';

$releaseId = trim((string) @file_get_contents($pointerFile));

if ($releaseId === '') {
    http_response_code(503);
    echo 'DeployPier: current release pointer is empty.';
    exit(1);
}

$releaseRoot = $basePath.'/releases/'.$releaseId;
$maintenance = $releaseRoot.'/storage/framework/maintenance.php';
$autoload = $releaseRoot.'/vendor/autoload.php';
$bootstrap = $releaseRoot.'/bootstrap/app.php';

if (is_file($maintenance)) {
    require $maintenance;
}

if (! is_file($autoload) || ! is_file($bootstrap)) {
    http_response_code(503);
    echo 'DeployPier: active release is incomplete.';
    exit(1);
}

require $autoload;
$app = require_once $bootstrap;
$app->usePublicPath(__DIR__);

return $app;
`, phpSingleQuoted(s.cfg.Runtime.AppRoot), phpSingleQuoted(s.cfg.Runtime.CurrentPointer))
}

func phpSingleQuoted(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `'`, `\'`)
	return replacer.Replace(value)
}

func (s *Service) ensureReleaseEnvironment(ctx context.Context, remoteReleasePath string) error {
	releaseEnvPath := joinServicePath(remoteReleasePath, ".env")
	releaseExists, err := s.transport.Exists(ctx, releaseEnvPath)
	if err != nil {
		return err
	}
	if releaseExists {
		return nil
	}

	sharedEnvPath := s.sharedRemoteEnvPath()
	sharedExists, err := s.transport.Exists(ctx, sharedEnvPath)
	if err != nil {
		return err
	}

	var envData []byte
	if sharedExists {
		envData, err = s.transport.ReadFile(ctx, sharedEnvPath)
		if err != nil {
			return err
		}
	} else {
		localEnvPath, localEnvData, ok, err := s.readLocalProductionEnv()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		envData = localEnvData
		if err := s.transport.WriteFile(ctx, sharedEnvPath, envData); err != nil {
			return status.Wrap(status.KindInternal, "seed shared remote .env from "+localEnvPath, err)
		}
	}

	if err := s.transport.WriteFile(ctx, releaseEnvPath, envData); err != nil {
		return status.Wrap(status.KindInternal, "write release .env", err)
	}
	return nil
}

func (s *Service) sharedRemoteEnvPath() string {
	return joinServicePath(s.cfg.Transport.Path, ".env")
}

func (s *Service) readLocalProductionEnv() (string, []byte, bool, error) {
	for _, candidate := range []string{"env.production", ".env.production"} {
		localPath := filepath.Join(s.cfg.Project.Root, candidate)
		data, err := os.ReadFile(localPath)
		if err == nil {
			return localPath, data, true, nil
		}
		if os.IsNotExist(err) {
			continue
		}
		return "", nil, false, status.Wrap(status.KindInternal, "read local production env", err)
	}
	return "", nil, false, nil
}

func (s *Service) remoteReleasePath(releaseID string) string {
	return joinServicePath(s.cfg.Remote.AppRoot, "releases", releaseID)
}

func (s *Service) remoteLockPath() string {
	return joinServicePath(s.cfg.Remote.AppRoot, ".deploypier", "locks", "deploy.lock")
}

func (s *Service) acquireRemoteLock(ctx context.Context, releaseID string) (func(), error) {
	lockPath := s.remoteLockPath()
	if err := s.transport.MkdirAll(ctx, serviceDir(lockPath)); err != nil {
		return nil, err
	}
	if err := s.transport.Mkdir(ctx, lockPath); err != nil {
		return nil, status.Wrap(status.KindConflict, "acquire remote deploy lock", err)
	}
	lockData := fmt.Sprintf("release_id=%s\ncreated_at=%s\n", releaseID, s.now().UTC().Format(time.RFC3339))
	if err := s.transport.WriteFile(ctx, joinServicePath(lockPath, "owner.txt"), []byte(lockData)); err != nil {
		_ = s.transport.RemoveAll(ctx, lockPath)
		return nil, err
	}
	return func() {
		_ = s.transport.RemoveAll(context.Background(), lockPath)
	}, nil
}

func (s *Service) validateRemoteRelease(ctx context.Context, release build.Release, remotePath string) error {
	raw, err := s.transport.ReadFile(ctx, joinServicePath(remotePath, "manifest.json"))
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
		info, err := s.transport.HashFile(ctx, joinServicePath(remotePath, file.Path))
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
	releasesRoot := joinServicePath(s.cfg.Remote.AppRoot, "releases")
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
			names = append(names, serviceBase(entry.Path))
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
		if err := s.transport.RemoveAll(ctx, joinServicePath(releasesRoot, name)); err != nil {
			return err
		}
	}
	return nil
}

func joinServicePath(root string, parts ...string) string {
	if strings.Contains(root, `\`) {
		all := append([]string{root}, parts...)
		return filepath.Clean(filepath.Join(all...))
	}
	all := append([]string{root}, parts...)
	return path.Clean(path.Join(all...))
}

func serviceDir(value string) string {
	if strings.Contains(value, `\`) {
		return filepath.Dir(value)
	}
	return path.Dir(value)
}

func serviceBase(value string) string {
	if strings.Contains(value, `\`) {
		return filepath.Base(value)
	}
	return path.Base(value)
}

func currentGitMetadata(ctx context.Context, dir string) (string, string) {
	return gitCommand(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD"), gitCommand(ctx, dir, "rev-parse", "HEAD")
}

func gitCommand(ctx context.Context, dir string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func canonicalRemoteCandidate(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	base := serviceBase(trimmed)
	if base == "public_html" || base == "app" {
		return serviceDir(trimmed)
	}
	return trimmed
}

func uniqueNonEmpty(values ...string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result
}
