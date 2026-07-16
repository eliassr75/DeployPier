package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/deploypier/deploypier/internal/build"
	"github.com/deploypier/deploypier/internal/config"
	"github.com/deploypier/deploypier/internal/status"
)

type UploadResult struct {
	RemotePath   string
	ManifestPath string
}

type UploadProgress struct {
	Path           string
	UploadedFiles  int
	TotalFiles     int
	UploadedBytes  int64
	TotalBytes     int64
	ManifestUpload bool
}

type UploadProgressFunc func(UploadProgress)

type FileInfo struct {
	Path      string
	Size      int64
	IsDir     bool
	IsSymlink bool
	SHA256    string
}

type Inspection struct {
	CurrentDir   string
	ResolvedPath string
}

type Transport interface {
	Name() string
	Probe(ctx context.Context) status.Report
	Inspect(ctx context.Context) (Inspection, error)
	UploadRelease(ctx context.Context, release build.Release, remotePath string, progress UploadProgressFunc) (UploadResult, error)
	ReadFile(ctx context.Context, remotePath string) ([]byte, error)
	WriteFile(ctx context.Context, remotePath string, data []byte) error
	Stat(ctx context.Context, remotePath string) (FileInfo, error)
	HashFile(ctx context.Context, remotePath string) (FileInfo, error)
	ReadDir(ctx context.Context, remotePath string) ([]FileInfo, error)
	Rename(ctx context.Context, fromPath string, toPath string) error
	Mkdir(ctx context.Context, remotePath string) error
	MkdirAll(ctx context.Context, remotePath string) error
	RemoveAll(ctx context.Context, remotePath string) error
	Exists(ctx context.Context, remotePath string) (bool, error)
}

func New(cfg config.TransportConfig) (Transport, error) {
	kind := normalizedKind(cfg)
	switch kind {
	case "local":
		return &LocalTransport{BasePath: cfg.Path}, nil
	case "sftp", "ssh":
		return NewSFTPTransport(cfg), nil
	case "ftps":
		return NewFTPSTransport(cfg), nil
	case "ftp":
		if !cfg.AllowInsecure {
			return nil, status.Wrap(status.KindConfig, "create transport", errors.New("plain FTP requires transport.allow_insecure=true"))
		}
		return NewFTPSTransport(cfg), nil
	default:
		return nil, status.Wrap(status.KindConfig, "create transport", errors.New("unsupported transport kind: "+kind))
	}
}

func normalizedKind(cfg config.TransportConfig) string {
	if strings.TrimSpace(cfg.Protocol) != "" {
		return strings.ToLower(strings.TrimSpace(cfg.Protocol))
	}
	return strings.ToLower(strings.TrimSpace(cfg.Kind))
}

type LocalTransport struct {
	BasePath string
}

func (t *LocalTransport) Name() string {
	return "local"
}

func (t *LocalTransport) Probe(ctx context.Context) status.Report {
	probeRoot := filepath.Join(t.BasePath, ".deploypier", "probe")
	if err := t.MkdirAll(ctx, probeRoot); err != nil {
		return status.Classify(status.Wrap(status.KindInternal, "probe local transport", err))
	}
	stage := filepath.Join(probeRoot, "stage")
	swapped := filepath.Join(probeRoot, "swapped")
	_ = os.RemoveAll(stage)
	_ = os.RemoveAll(swapped)
	if err := t.Mkdir(ctx, stage); err != nil {
		return status.Classify(status.Wrap(status.KindInternal, "probe local transport", err))
	}
	if err := t.Rename(ctx, stage, swapped); err != nil {
		return status.Classify(status.Wrap(status.KindUnsupported, "probe local transport rename", err))
	}
	_ = t.RemoveAll(ctx, swapped)
	return status.Report{
		Level:   status.LevelOK,
		Code:    "ok",
		Message: "local transport ready",
	}
}

func (t *LocalTransport) Inspect(_ context.Context) (Inspection, error) {
	resolved := filepath.Clean(t.BasePath)
	return Inspection{
		CurrentDir:   resolved,
		ResolvedPath: resolved,
	}, nil
}

func (t *LocalTransport) UploadRelease(ctx context.Context, release build.Release, remotePath string, progress UploadProgressFunc) (UploadResult, error) {
	if exists, err := t.Exists(ctx, remotePath); err != nil {
		return UploadResult{}, err
	} else if exists && !release.AllowExistingRemote {
		return UploadResult{}, status.Wrap(status.KindConflict, "upload release", errors.New("release already exists remotely"))
	} else if !exists {
		if err := os.MkdirAll(remotePath, 0o755); err != nil {
			return UploadResult{}, status.Wrap(status.KindInternal, "create remote release directory", err)
		}
	}
	if strings.TrimSpace(release.ArchivePath) != "" {
		if err := uploadArchiveRelease(ctx, release, remotePath, t.writeLocalFile, progress); err != nil {
			return UploadResult{}, err
		}
		return UploadResult{
			RemotePath:   remotePath,
			ManifestPath: filepath.Join(remotePath, "manifest.json"),
		}, nil
	}
	if err := uploadReleaseTree(ctx, release, remotePath, t.writeLocalFile, progress); err != nil {
		return UploadResult{}, err
	}
	return UploadResult{
		RemotePath:   remotePath,
		ManifestPath: filepath.Join(remotePath, "manifest.json"),
	}, nil
}

func (t *LocalTransport) ReadFile(_ context.Context, remotePath string) ([]byte, error) {
	data, err := os.ReadFile(remotePath)
	if err != nil {
		return nil, status.Wrap(status.KindNotFound, "read local remote file", err)
	}
	return data, nil
}

func (t *LocalTransport) WriteFile(_ context.Context, remotePath string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(remotePath), 0o755); err != nil {
		return status.Wrap(status.KindInternal, "prepare local remote directory", err)
	}
	if err := os.WriteFile(remotePath, data, 0o644); err != nil {
		return status.Wrap(status.KindInternal, "write local remote file", err)
	}
	return nil
}

func (t *LocalTransport) Stat(_ context.Context, remotePath string) (FileInfo, error) {
	info, err := os.Lstat(remotePath)
	if err != nil {
		return FileInfo{}, status.Wrap(status.KindNotFound, "stat local remote path", err)
	}
	return FileInfo{
		Path:      remotePath,
		Size:      info.Size(),
		IsDir:     info.IsDir(),
		IsSymlink: info.Mode()&os.ModeSymlink != 0,
	}, nil
}

func (t *LocalTransport) HashFile(_ context.Context, remotePath string) (FileInfo, error) {
	info, err := t.Stat(context.Background(), remotePath)
	if err != nil {
		return FileInfo{}, err
	}
	if info.IsDir {
		return info, nil
	}
	file, err := os.Open(remotePath)
	if err != nil {
		return FileInfo{}, status.Wrap(status.KindNotFound, "open local remote file", err)
	}
	defer file.Close()

	sum, err := hashStream(file)
	if err != nil {
		return FileInfo{}, status.Wrap(status.KindInternal, "hash local remote file", err)
	}
	info.SHA256 = sum
	return info, nil
}

func (t *LocalTransport) ReadDir(_ context.Context, remotePath string) ([]FileInfo, error) {
	entries, err := os.ReadDir(remotePath)
	if err != nil {
		return nil, status.Wrap(status.KindNotFound, "read local remote dir", err)
	}
	result := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, status.Wrap(status.KindInternal, "read local remote entry", err)
		}
		result = append(result, FileInfo{
			Path:      filepath.Join(remotePath, entry.Name()),
			Size:      info.Size(),
			IsDir:     entry.IsDir(),
			IsSymlink: info.Mode()&os.ModeSymlink != 0,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Path < result[j].Path
	})
	return result, nil
}

func (t *LocalTransport) Rename(_ context.Context, fromPath string, toPath string) error {
	if err := os.MkdirAll(filepath.Dir(toPath), 0o755); err != nil {
		return status.Wrap(status.KindInternal, "prepare rename target", err)
	}
	if err := os.Rename(fromPath, toPath); err != nil {
		return status.Wrap(status.KindInternal, "rename local remote path", err)
	}
	return nil
}

func (t *LocalTransport) Mkdir(_ context.Context, remotePath string) error {
	if err := os.Mkdir(remotePath, 0o755); err != nil {
		return status.Wrap(status.KindInternal, "mkdir local remote path", err)
	}
	return nil
}

func (t *LocalTransport) MkdirAll(_ context.Context, remotePath string) error {
	if err := os.MkdirAll(remotePath, 0o755); err != nil {
		return status.Wrap(status.KindInternal, "mkdirall local remote path", err)
	}
	return nil
}

func (t *LocalTransport) RemoveAll(_ context.Context, remotePath string) error {
	if err := os.RemoveAll(remotePath); err != nil {
		return status.Wrap(status.KindInternal, "remove local remote path", err)
	}
	return nil
}

func (t *LocalTransport) Exists(_ context.Context, remotePath string) (bool, error) {
	_, err := os.Lstat(remotePath)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, status.Wrap(status.KindInternal, "stat local remote existence", err)
}

func (t *LocalTransport) writeLocalFile(localPath string, remotePath string, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(remotePath), 0o755); err != nil {
		return status.Wrap(status.KindInternal, "prepare local upload directory", err)
	}
	in, err := os.Open(localPath)
	if err != nil {
		return status.Wrap(status.KindNotFound, "open local upload source", err)
	}
	defer in.Close()

	out, err := os.Create(remotePath)
	if err != nil {
		return status.Wrap(status.KindInternal, "create local upload target", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return status.Wrap(status.KindInternal, "copy local upload file", err)
	}
	if mode != 0 {
		if err := out.Chmod(mode); err != nil {
			return status.Wrap(status.KindInternal, "chmod local upload target", err)
		}
	}
	return nil
}

type uploadWriter func(localPath string, remotePath string, mode fs.FileMode) error

func uploadReleaseTree(ctx context.Context, release build.Release, remotePath string, writer uploadWriter, progress UploadProgressFunc) error {
	manifestInfo, err := os.Stat(release.ManifestPath)
	if err != nil {
		return status.Wrap(status.KindInternal, "stat release manifest", err)
	}

	totalFiles := len(release.Manifest.Files) + 1
	var totalBytes int64 = manifestInfo.Size()
	for _, file := range release.Manifest.Files {
		totalBytes += file.Size
	}

	uploadedFiles := 0
	var uploadedBytes int64
	for _, file := range release.Manifest.Files {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		localPath := filepath.Join(release.BundlePath, filepath.FromSlash(file.Path))
		if err := writer(localPath, joinRemotePath(remotePath, file.Path), 0o644); err != nil {
			return err
		}
		uploadedFiles++
		uploadedBytes += file.Size
		if progress != nil {
			progress(UploadProgress{
				Path:          file.Path,
				UploadedFiles: uploadedFiles,
				TotalFiles:    totalFiles,
				UploadedBytes: uploadedBytes,
				TotalBytes:    totalBytes,
			})
		}
	}

	if err := writer(release.ManifestPath, joinRemotePath(remotePath, "manifest.json"), 0o644); err != nil {
		return err
	}
	if progress != nil {
		progress(UploadProgress{
			Path:           "manifest.json",
			UploadedFiles:  totalFiles,
			TotalFiles:     totalFiles,
			UploadedBytes:  totalBytes,
			TotalBytes:     totalBytes,
			ManifestUpload: true,
		})
	}
	return nil
}

func uploadArchiveRelease(ctx context.Context, release build.Release, remotePath string, writer uploadWriter, progress UploadProgressFunc) error {
	archiveInfo, err := os.Stat(release.ArchivePath)
	if err != nil {
		return status.Wrap(status.KindInternal, "stat release archive", err)
	}
	manifestInfo, err := os.Stat(release.ManifestPath)
	if err != nil {
		return status.Wrap(status.KindInternal, "stat release manifest", err)
	}

	totalFiles := 2
	totalBytes := archiveInfo.Size() + manifestInfo.Size()
	uploadedFiles := 0
	var uploadedBytes int64

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if err := writer(release.ArchivePath, joinRemotePath(remotePath, "release.zip"), 0o644); err != nil {
		return err
	}
	uploadedFiles++
	uploadedBytes += archiveInfo.Size()
	if progress != nil {
		progress(UploadProgress{
			Path:          "release.zip",
			UploadedFiles: uploadedFiles,
			TotalFiles:    totalFiles,
			UploadedBytes: uploadedBytes,
			TotalBytes:    totalBytes,
		})
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if err := writer(release.ManifestPath, joinRemotePath(remotePath, "manifest.json"), 0o644); err != nil {
		return err
	}
	if progress != nil {
		progress(UploadProgress{
			Path:           "manifest.json",
			UploadedFiles:  totalFiles,
			TotalFiles:     totalFiles,
			UploadedBytes:  totalBytes,
			TotalBytes:     totalBytes,
			ManifestUpload: true,
		})
	}

	return nil
}

func joinRemotePath(root string, relativePath string) string {
	if strings.Contains(root, `\`) {
		return filepath.Join(root, filepath.FromSlash(relativePath))
	}
	return path.Join(path.Clean(root), relativePath)
}

func hashStream(reader io.Reader) (string, error) {
	hasher := sha256.New()
	if _, err := io.Copy(hasher, reader); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
