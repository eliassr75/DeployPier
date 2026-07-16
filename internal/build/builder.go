package build

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/deploypier/deploypier/internal/config"
	"github.com/deploypier/deploypier/internal/status"
)

type Builder struct {
	Now func() time.Time
}

type Release struct {
	ID                  string
	Path                string
	BundlePath          string
	ManifestPath        string
	ArchivePath         string
	AllowExistingRemote bool
	Manifest            Manifest
}

type Manifest struct {
	ReleaseID string         `json:"release_id"`
	Project   string         `json:"project"`
	Framework string         `json:"framework"`
	BuiltAt   string         `json:"built_at"`
	Includes  []string       `json:"includes"`
	Excludes  []string       `json:"excludes"`
	Files     []ManifestFile `json:"files"`
	Skipped   []string       `json:"skipped"`
	SHA256    string         `json:"sha256"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func NewBuilder() *Builder {
	return &Builder{
		Now: time.Now,
	}
}

func (b *Builder) Build(ctx context.Context, cfg config.Config) (Release, error) {
	if err := runCommand(ctx, cfg.Project.Root, cfg.Build.PHPCommand); err != nil {
		return Release{}, err
	}
	if err := runCommand(ctx, cfg.Project.Root, cfg.Build.NodeCommand); err != nil {
		return Release{}, err
	}

	releaseID := b.Now().UTC().Format("20060102T150405Z")
	releasePath := filepath.Join(cfg.Release.Directory, releaseID)
	bundlePath := filepath.Join(releasePath, "bundle")
	manifestPath := filepath.Join(releasePath, "manifest.json")

	if err := os.MkdirAll(bundlePath, 0o755); err != nil {
		return Release{}, status.Wrap(status.KindInternal, "create release directory", err)
	}

	includePatterns := cfg.Build.Include
	excludePatterns := append([]string{}, cfg.Build.Exclude...)
	excludePatterns = append(excludePatterns, deriveSelfExcludes(cfg)...)

	manifest := Manifest{
		ReleaseID: releaseID,
		Project:   cfg.Project.Name,
		Framework: cfg.Project.Framework,
		BuiltAt:   b.Now().UTC().Format(time.RFC3339),
		Includes:  append([]string{}, includePatterns...),
		Excludes:  append([]string{}, excludePatterns...),
	}

	if err := filepath.WalkDir(cfg.Project.Root, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		rel, err := filepath.Rel(cfg.Project.Root, current)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		normalized := normalize(rel)
		if shouldExclude(normalized, excludePatterns) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			manifest.Skipped = append(manifest.Skipped, normalized)
			return nil
		}
		if !shouldInclude(normalized, includePatterns) {
			return nil
		}

		targetPath := filepath.Join(bundlePath, filepath.FromSlash(normalized))
		record, err := copyWithHash(current, targetPath, normalized)
		if err != nil {
			return err
		}
		manifest.Files = append(manifest.Files, record)
		return nil
	}); err != nil {
		return Release{}, status.Wrap(status.KindInternal, "walk project tree", err)
	}

	sort.Slice(manifest.Files, func(i, j int) bool {
		return manifest.Files[i].Path < manifest.Files[j].Path
	})
	sort.Strings(manifest.Skipped)
	manifest.SHA256 = aggregateHash(manifest.Files)

	serialized, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Release{}, status.Wrap(status.KindInternal, "marshal manifest", err)
	}
	if err := os.WriteFile(manifestPath, serialized, 0o644); err != nil {
		return Release{}, status.Wrap(status.KindInternal, "write manifest", err)
	}

	archivePath := ""
	if cfg.Release.UploadMode == "archive" {
		archivePath = filepath.Join(releasePath, "release.zip")
		if err := WriteArchive(archivePath, bundlePath, manifest.Files); err != nil {
			return Release{}, err
		}
	}

	if err := pruneReleases(cfg.Release.Directory, cfg.Release.Retain); err != nil {
		return Release{}, status.Wrap(status.KindInternal, "prune retained releases", err)
	}

	return Release{
		ID:           releaseID,
		Path:         releasePath,
		BundlePath:   bundlePath,
		ManifestPath: manifestPath,
		ArchivePath:  archivePath,
		Manifest:     manifest,
	}, nil
}

func aggregateHash(files []ManifestFile) string {
	hasher := sha256.New()
	for _, file := range files {
		_, _ = io.WriteString(hasher, file.Path)
		_, _ = io.WriteString(hasher, ":")
		_, _ = io.WriteString(hasher, file.SHA256)
		_, _ = io.WriteString(hasher, "\n")
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func runCommand(ctx context.Context, dir string, command string) error {
	if strings.TrimSpace(command) == "" {
		return nil
	}

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-lc", command)
	}
	cmd.Dir = dir

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		output := strings.TrimSpace(strings.Join([]string{stdout.String(), stderr.String()}, "\n"))
		return status.Wrap(status.KindInternal, "run build command", fmt.Errorf("%s failed: %w\n%s", command, err, output))
	}

	return nil
}

func (b *Builder) Load(_ context.Context, cfg config.Config, releaseID string) (Release, error) {
	releasePath := filepath.Join(cfg.Release.Directory, releaseID)
	manifestPath := filepath.Join(releasePath, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return Release{}, status.Wrap(status.KindNotFound, "load release manifest", err)
	}

	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return Release{}, status.Wrap(status.KindInternal, "decode release manifest", err)
	}

	return Release{
		ID:           releaseID,
		Path:         releasePath,
		BundlePath:   filepath.Join(releasePath, "bundle"),
		ManifestPath: manifestPath,
		ArchivePath:  filepath.Join(releasePath, "release.zip"),
		Manifest:     manifest,
	}, nil
}

func WriteArchive(targetPath string, bundlePath string, files []ManifestFile) error {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return status.Wrap(status.KindInternal, "prepare archive directory", err)
	}

	file, err := os.Create(targetPath)
	if err != nil {
		return status.Wrap(status.KindInternal, "create release archive", err)
	}
	defer file.Close()

	archive := zip.NewWriter(file)

	for _, entry := range files {
		sourcePath := filepath.Join(bundlePath, filepath.FromSlash(entry.Path))
		if err := appendArchiveFile(archive, sourcePath, entry); err != nil {
			_ = archive.Close()
			return err
		}
	}

	if err := archive.Close(); err != nil {
		return status.Wrap(status.KindInternal, "finalize release archive", err)
	}

	return nil
}

func appendArchiveFile(archive *zip.Writer, sourcePath string, entry ManifestFile) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return status.Wrap(status.KindNotFound, "open archive source file", err)
	}
	defer source.Close()

	info, err := source.Stat()
	if err != nil {
		return status.Wrap(status.KindInternal, "stat archive source file", err)
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return status.Wrap(status.KindInternal, "create archive header", err)
	}
	header.Name = filepath.ToSlash(entry.Path)
	header.Method = zip.Deflate

	writer, err := archive.CreateHeader(header)
	if err != nil {
		return status.Wrap(status.KindInternal, "create archive entry", err)
	}

	if _, err := io.Copy(writer, source); err != nil {
		return status.Wrap(status.KindInternal, "write archive entry", err)
	}

	return nil
}

func deriveSelfExcludes(cfg config.Config) []string {
	var patterns []string
	for _, candidate := range []string{cfg.Release.Directory, cfg.State.File} {
		rel, ok := relativePattern(cfg.Project.Root, candidate)
		if !ok {
			continue
		}
		patterns = append(patterns, rel)
		if !strings.HasSuffix(rel, "/**") && !strings.Contains(filepath.Base(candidate), ".") {
			patterns = append(patterns, rel+"/**")
		}
	}
	return patterns
}

func relativePattern(root, target string) (string, bool) {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", false
	}
	if strings.HasPrefix(rel, "..") {
		return "", false
	}
	return normalize(rel), true
}

func copyWithHash(sourcePath, targetPath, relativePath string) (ManifestFile, error) {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return ManifestFile{}, status.Wrap(status.KindInternal, "create target directory", err)
	}

	source, err := os.Open(sourcePath)
	if err != nil {
		return ManifestFile{}, status.Wrap(status.KindInternal, "open source file", err)
	}
	defer source.Close()

	info, err := source.Stat()
	if err != nil {
		return ManifestFile{}, status.Wrap(status.KindInternal, "stat source file", err)
	}

	target, err := os.Create(targetPath)
	if err != nil {
		return ManifestFile{}, status.Wrap(status.KindInternal, "create target file", err)
	}
	defer target.Close()

	hasher := sha256.New()
	writer := io.MultiWriter(target, hasher)
	if _, err := io.Copy(writer, source); err != nil {
		return ManifestFile{}, status.Wrap(status.KindInternal, "copy file", err)
	}

	if err := target.Chmod(info.Mode()); err != nil {
		return ManifestFile{}, status.Wrap(status.KindInternal, "chmod target file", err)
	}

	return ManifestFile{
		Path:   relativePath,
		Size:   info.Size(),
		SHA256: hex.EncodeToString(hasher.Sum(nil)),
	}, nil
}

func shouldInclude(candidate string, includePatterns []string) bool {
	if len(includePatterns) == 0 {
		return true
	}
	return matches(includePatterns, candidate)
}

func shouldExclude(candidate string, excludePatterns []string) bool {
	return matches(excludePatterns, candidate)
}

func matches(patterns []string, candidate string) bool {
	for _, raw := range patterns {
		pattern := normalize(raw)
		if pattern == "" {
			continue
		}
		if pattern == candidate {
			return true
		}
		if strings.HasSuffix(pattern, "/") {
			prefix := strings.TrimSuffix(pattern, "/")
			if candidate == prefix || strings.HasPrefix(candidate, prefix+"/") {
				return true
			}
		}
		if strings.HasSuffix(pattern, "/**") {
			prefix := strings.TrimSuffix(pattern, "/**")
			if candidate == prefix || strings.HasPrefix(candidate, prefix+"/") {
				return true
			}
		}
		if ok, _ := path.Match(pattern, candidate); ok {
			return true
		}
		if strings.Contains(pattern, "**") {
			fallback := strings.ReplaceAll(pattern, "**", "*")
			if ok, _ := path.Match(fallback, candidate); ok {
				return true
			}
		}
	}
	return false
}

func normalize(value string) string {
	return filepath.ToSlash(filepath.Clean(value))
}

func pruneReleases(baseDir string, retain int) error {
	if retain < 1 {
		return nil
	}

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return err
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	if len(names) <= retain {
		return nil
	}

	for _, name := range names[:len(names)-retain] {
		if err := os.RemoveAll(filepath.Join(baseDir, name)); err != nil {
			return err
		}
	}
	return nil
}
