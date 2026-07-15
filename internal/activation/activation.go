package activation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/deploypier/deploypier/internal/config"
	"github.com/deploypier/deploypier/internal/status"
	"github.com/deploypier/deploypier/internal/transport"
)

type Result struct {
	ReleaseID   string
	ReleasePath string
	PublicPath  string
	Mode        string
	Degraded    bool
	Message     string
}

type Activator interface {
	Name() string
	Current(ctx context.Context) (string, error)
	Previous(ctx context.Context) (string, error)
	Activate(ctx context.Context, releaseID string, reason string) (Result, error)
}

type remoteState struct {
	Current     string             `json:"current"`
	Previous    string             `json:"previous"`
	UpdatedAt   string             `json:"updated_at"`
	Activations []activationRecord `json:"activations"`
}

type activationRecord struct {
	ReleaseID   string `json:"release_id"`
	Reason      string `json:"reason"`
	ActivatedAt string `json:"activated_at"`
	Mode        string `json:"mode"`
}

func New(cfg config.ActivationConfig, remote config.RemoteConfig, fs transport.Transport) (Activator, error) {
	switch cfg.Kind {
	case "pointer":
		return &PointerActivator{
			CurrentPointer: cfg.CurrentPointer,
			Remote:         remote,
			FS:             fs,
			Now:            time.Now,
		}, nil
	default:
		return nil, status.Wrap(status.KindConfig, "create activator", errors.New("unsupported activation kind: "+cfg.Kind))
	}
}

type PointerActivator struct {
	CurrentPointer string
	Remote         config.RemoteConfig
	FS             transport.Transport
	Now            func() time.Time
}

func (a *PointerActivator) Name() string {
	return "pointer"
}

func (a *PointerActivator) Current(ctx context.Context) (string, error) {
	raw, err := a.FS.ReadFile(ctx, a.CurrentPointer)
	if err != nil {
		return "", status.Wrap(status.KindNotFound, "read remote current pointer", err)
	}
	value := strings.TrimSpace(string(raw))
	if value == "" {
		return "", status.Wrap(status.KindNotFound, "read remote current pointer", errors.New("current pointer is empty"))
	}
	return value, nil
}

func (a *PointerActivator) Previous(ctx context.Context) (string, error) {
	state, err := a.loadState(ctx)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(state.Previous) == "" {
		return "", status.Wrap(status.KindNotFound, "load previous release", errors.New("previous release not recorded"))
	}
	return state.Previous, nil
}

func (a *PointerActivator) Activate(ctx context.Context, releaseID string, reason string) (Result, error) {
	mode := a.Remote.Layout
	switch mode {
	case "", "auto":
		result, err := a.activateReleaseBased(ctx, releaseID, reason)
		if err == nil {
			return result, nil
		}
		inPlaceResult, fallbackErr := a.activateInPlace(ctx, releaseID, reason)
		if fallbackErr != nil {
			return Result{}, err
		}
		inPlaceResult.Mode = "in-place"
		inPlaceResult.Degraded = true
		inPlaceResult.Message = "release-based activation failed; in-place fallback applied"
		return inPlaceResult, nil
	case "release-based":
		return a.activateReleaseBased(ctx, releaseID, reason)
	case "in-place":
		return a.activateInPlace(ctx, releaseID, reason)
	default:
		return Result{}, status.Wrap(status.KindConfig, "select activation mode", fmt.Errorf("unsupported remote layout: %s", mode))
	}
}

func (a *PointerActivator) activateReleaseBased(ctx context.Context, releaseID string, reason string) (Result, error) {
	releasePath := a.releasePath(releaseID)
	if err := a.validatePublicIndex(ctx); err != nil {
		return Result{}, err
	}
	if err := a.syncActivePublic(ctx, releaseID); err != nil {
		return Result{}, err
	}
	if err := a.persistActivation(ctx, releaseID, reason, "release-based"); err != nil {
		return Result{}, err
	}

	return Result{
		ReleaseID:   releaseID,
		ReleasePath: releasePath,
		PublicPath:  a.Remote.PublicRoot,
		Mode:        "release-based",
		Message:     "current pointer updated and public assets synchronized",
	}, nil
}

func (a *PointerActivator) activateInPlace(ctx context.Context, releaseID string, reason string) (Result, error) {
	if err := a.validatePublicIndex(ctx); err != nil {
		return Result{}, err
	}
	if err := a.syncActivePublic(ctx, releaseID); err != nil {
		return Result{}, err
	}
	if err := a.persistActivation(ctx, releaseID, reason, "in-place"); err != nil {
		return Result{}, err
	}
	return Result{
		ReleaseID:   releaseID,
		ReleasePath: a.releasePath(releaseID),
		PublicPath:  a.Remote.PublicRoot,
		Mode:        "in-place",
		Message:     "current pointer updated and public assets synchronized in place",
	}, nil
}

func (a *PointerActivator) syncActivePublic(ctx context.Context, releaseID string) error {
	releasePublicRoot := joinActivationPath(a.releasePath(releaseID), "public")
	if err := a.ensureReleaseReady(ctx, releaseID); err != nil {
		return err
	}
	if err := a.FS.MkdirAll(ctx, a.Remote.PublicRoot); err != nil {
		return err
	}
	return a.syncPublicTree(ctx, releasePublicRoot, a.Remote.PublicRoot, true)
}

func (a *PointerActivator) syncPublicTree(ctx context.Context, sourceRoot string, targetRoot string, inPlace bool) error {
	entries, err := a.FS.ReadDir(ctx, sourceRoot)
	if err != nil {
		return status.Wrap(status.KindNotFound, "read release public dir", err)
	}

	if inPlace {
		if err := a.prunePublicRoot(ctx, sourceRoot, targetRoot); err != nil {
			return err
		}
	}

	for _, entry := range entries {
		relativePath := strings.TrimPrefix(entry.Path, sourceRoot)
		relativePath = strings.TrimLeft(relativePath, `/\`)
		relativePath = strings.TrimSpace(relativePath)
		if relativePath == "" || relativePath == "index.php" || relativePath == "storage" {
			continue
		}
		targetPath := joinActivationPath(targetRoot, relativePath)
		if entry.IsDir {
			if err := a.FS.MkdirAll(ctx, targetPath); err != nil {
				return err
			}
			if err := a.syncPublicTree(ctx, entry.Path, targetPath, false); err != nil {
				return err
			}
			continue
		}
		data, err := a.FS.ReadFile(ctx, entry.Path)
		if err != nil {
			return err
		}
		if err := a.FS.WriteFile(ctx, targetPath, data); err != nil {
			return err
		}
	}
	return nil
}

func (a *PointerActivator) prunePublicRoot(ctx context.Context, releasePublicRoot string, publicRoot string) error {
	currentEntries, err := a.FS.ReadDir(ctx, publicRoot)
	if err != nil {
		return nil
	}
	expected := make(map[string]struct{})
	if err := a.collectReleasePublicPaths(ctx, releasePublicRoot, releasePublicRoot, expected); err != nil {
		return err
	}
	expected["index.php"] = struct{}{}
	expected["storage"] = struct{}{}

	for _, entry := range currentEntries {
		name := baseActivationPath(entry.Path)
		if _, ok := expected[name]; ok {
			continue
		}
		if err := a.FS.RemoveAll(ctx, entry.Path); err != nil {
			return err
		}
	}
	return nil
}

func (a *PointerActivator) collectReleasePublicPaths(ctx context.Context, root string, current string, names map[string]struct{}) error {
	entries, err := a.FS.ReadDir(ctx, current)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		relativePath := strings.TrimPrefix(entry.Path, root)
		relativePath = strings.TrimLeft(relativePath, `/\`)
		if relativePath == "" || relativePath == "index.php" || relativePath == "storage" {
			continue
		}
		names[baseActivationPath(relativePath)] = struct{}{}
		if entry.IsDir {
			if err := a.collectReleasePublicPaths(ctx, root, entry.Path, names); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *PointerActivator) ensureReleaseReady(ctx context.Context, releaseID string) error {
	requiredPaths := []string{
		a.releasePath(releaseID),
		joinActivationPath(a.releasePath(releaseID), "bootstrap", "app.php"),
		joinActivationPath(a.releasePath(releaseID), "vendor", "autoload.php"),
		joinActivationPath(a.releasePath(releaseID), "public"),
	}
	for _, required := range requiredPaths {
		exists, err := a.FS.Exists(ctx, required)
		if err != nil {
			return err
		}
		if !exists {
			return status.Wrap(status.KindNotFound, "validate remote release", fmt.Errorf("missing remote release path: %s", required))
		}
	}
	return nil
}

func (a *PointerActivator) validatePublicIndex(ctx context.Context) error {
	indexPath := joinActivationPath(a.Remote.PublicRoot, "index.php")
	exists, err := a.FS.Exists(ctx, indexPath)
	if err != nil {
		return err
	}
	if !exists {
		return status.Wrap(status.KindConflict, "validate remote public index", fmt.Errorf("missing %s; generate a stable index.php that reads %s before activating", indexPath, a.CurrentPointer))
	}

	raw, err := a.FS.ReadFile(ctx, indexPath)
	if err != nil {
		return status.Wrap(status.KindConflict, "read remote public index", err)
	}

	content := string(raw)
	markers := []string{
		baseActivationPath(a.CurrentPointer),
		"usePublicPath",
		"/releases/",
	}
	for _, marker := range markers {
		if !strings.Contains(content, marker) {
			return status.Wrap(status.KindConflict, "validate remote public index", fmt.Errorf("%s is not compatible with DeployPier current pointer mode; missing marker %q", indexPath, marker))
		}
	}
	return nil
}

func (a *PointerActivator) persistActivation(ctx context.Context, releaseID string, reason string, mode string) error {
	state, _ := a.loadState(ctx)
	previous := state.Current
	if previous == releaseID {
		previous = state.Previous
	}
	state.Previous = previous
	state.Current = releaseID
	state.UpdatedAt = a.Now().UTC().Format(time.RFC3339)
	state.Activations = append(state.Activations, activationRecord{
		ReleaseID:   releaseID,
		Reason:      reason,
		ActivatedAt: state.UpdatedAt,
		Mode:        mode,
	})
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return status.Wrap(status.KindInternal, "marshal activation state", err)
	}
	if err := a.atomicWrite(ctx, a.CurrentPointer, []byte(releaseID+"\n")); err != nil {
		return err
	}
	if err := a.atomicWrite(ctx, a.statePath(), raw); err != nil {
		return err
	}
	return nil
}

func (a *PointerActivator) loadState(ctx context.Context) (remoteState, error) {
	raw, err := a.FS.ReadFile(ctx, a.statePath())
	if err != nil {
		return remoteState{}, status.Wrap(status.KindNotFound, "read activation state", err)
	}
	var state remoteState
	if err := json.Unmarshal(raw, &state); err != nil {
		return remoteState{}, status.Wrap(status.KindInternal, "decode activation state", err)
	}
	return state, nil
}

func (a *PointerActivator) atomicWrite(ctx context.Context, targetPath string, data []byte) error {
	tempPath := targetPath + ".tmp"
	backupPath := targetPath + ".bak"
	if err := a.FS.WriteFile(ctx, tempPath, data); err != nil {
		return err
	}
	exists, err := a.FS.Exists(ctx, targetPath)
	if err != nil {
		return err
	}
	if exists {
		_ = a.FS.RemoveAll(ctx, backupPath)
		if err := a.FS.Rename(ctx, targetPath, backupPath); err != nil {
			return err
		}
	}
	if err := a.FS.Rename(ctx, tempPath, targetPath); err != nil {
		if exists {
			_ = a.FS.Rename(ctx, backupPath, targetPath)
		}
		return err
	}
	if exists {
		_ = a.FS.RemoveAll(ctx, backupPath)
	}
	return nil
}

func (a *PointerActivator) releasePath(releaseID string) string {
	return joinActivationPath(a.Remote.AppRoot, "releases", releaseID)
}

func (a *PointerActivator) statePath() string {
	return joinActivationPath(dirActivationPath(a.CurrentPointer), "releases.json")
}

func joinActivationPath(root string, parts ...string) string {
	if strings.Contains(root, `\`) || filepath.VolumeName(root) != "" {
		all := append([]string{root}, parts...)
		return filepath.Clean(filepath.Join(all...))
	}
	all := append([]string{root}, parts...)
	return path.Clean(path.Join(all...))
}

func dirActivationPath(value string) string {
	if strings.Contains(value, `\`) || filepath.VolumeName(value) != "" {
		return filepath.Dir(value)
	}
	return path.Dir(value)
}

func baseActivationPath(value string) string {
	if strings.Contains(value, `\`) || filepath.VolumeName(value) != "" {
		return filepath.Base(value)
	}
	return path.Base(value)
}
