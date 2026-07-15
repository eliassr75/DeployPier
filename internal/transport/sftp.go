package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deploypier/deploypier/internal/build"
	"github.com/deploypier/deploypier/internal/config"
	"github.com/deploypier/deploypier/internal/status"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type SFTPTransport struct {
	Host           string
	Port           int
	User           string
	Password       string
	PrivateKey     string
	BasePath       string
	KnownHostsPath string
	Insecure       bool

	mu        sync.Mutex
	sshClient *ssh.Client
	client    *sftp.Client
}

func NewSFTPTransport(cfg config.TransportConfig) *SFTPTransport {
	return &SFTPTransport{
		Host:           cfg.Host,
		Port:           cfg.Port,
		User:           cfg.User,
		Password:       cfg.Password,
		PrivateKey:     cfg.PrivateKey,
		BasePath:       cfg.Path,
		KnownHostsPath: cfg.KnownHosts,
		Insecure:       cfg.AllowInsecure,
	}
}

func (t *SFTPTransport) Name() string {
	if t.Insecure {
		return "sftp-insecure"
	}
	return "sftp"
}

func (t *SFTPTransport) Probe(ctx context.Context) status.Report {
	if err := t.probeFilesystem(ctx); err != nil {
		return status.Classify(err)
	}
	if t.Insecure {
		return status.Report{
			Level:   status.LevelWarn,
			Code:    "insecure_sftp",
			Message: "sftp is connected with host key verification disabled",
		}
	}
	return status.Report{
		Level:   status.LevelOK,
		Code:    "ok",
		Message: "sftp transport ready",
	}
}

func (t *SFTPTransport) Inspect(ctx context.Context) (Inspection, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	client, err := t.connectLocked(ctx)
	if err != nil {
		return Inspection{}, err
	}

	currentDir, err := client.Getwd()
	if err != nil {
		return Inspection{}, status.Wrap(status.KindInternal, "inspect sftp current dir", err)
	}

	resolvedPath := cleanRemote(t.BasePath)
	if strings.TrimSpace(t.BasePath) != "" {
		if realPath, err := client.RealPath(t.BasePath); err == nil {
			resolvedPath = cleanRemote(realPath)
		}
	}

	return Inspection{
		CurrentDir:   cleanRemote(currentDir),
		ResolvedPath: resolvedPath,
	}, nil
}

func (t *SFTPTransport) UploadRelease(ctx context.Context, release build.Release, remotePath string) (UploadResult, error) {
	if exists, err := t.Exists(ctx, remotePath); err != nil {
		return UploadResult{}, err
	} else if exists {
		return UploadResult{}, status.Wrap(status.KindConflict, "upload sftp release", errors.New("release already exists remotely"))
	}
	if err := t.MkdirAll(ctx, remotePath); err != nil {
		return UploadResult{}, err
	}
	if err := uploadReleaseTree(ctx, release, remotePath, t.writeRemoteFile); err != nil {
		return UploadResult{}, err
	}
	return UploadResult{
		RemotePath:   remotePath,
		ManifestPath: path.Join(cleanRemote(remotePath), "manifest.json"),
	}, nil
}

func (t *SFTPTransport) ReadFile(ctx context.Context, remotePath string) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	client, err := t.connectLocked(ctx)
	if err != nil {
		return nil, err
	}
	file, err := client.Open(cleanRemote(remotePath))
	if err != nil {
		return nil, status.Wrap(status.KindNotFound, "open sftp file", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, status.Wrap(status.KindInternal, "read sftp file", err)
	}
	return data, nil
}

func (t *SFTPTransport) WriteFile(ctx context.Context, remotePath string, data []byte) error {
	return t.writeBytes(ctx, remotePath, data)
}

func (t *SFTPTransport) Stat(ctx context.Context, remotePath string) (FileInfo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	client, err := t.connectLocked(ctx)
	if err != nil {
		return FileInfo{}, err
	}
	info, err := client.Lstat(cleanRemote(remotePath))
	if err != nil {
		return FileInfo{}, status.Wrap(status.KindNotFound, "stat sftp path", err)
	}
	return FileInfo{
		Path:      cleanRemote(remotePath),
		Size:      info.Size(),
		IsDir:     info.IsDir(),
		IsSymlink: info.Mode()&os.ModeSymlink != 0,
	}, nil
}

func (t *SFTPTransport) HashFile(ctx context.Context, remotePath string) (FileInfo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	client, err := t.connectLocked(ctx)
	if err != nil {
		return FileInfo{}, err
	}
	file, err := client.Open(cleanRemote(remotePath))
	if err != nil {
		return FileInfo{}, status.Wrap(status.KindNotFound, "open sftp file for hash", err)
	}
	defer file.Close()

	sum, err := hashStream(file)
	if err != nil {
		return FileInfo{}, status.Wrap(status.KindInternal, "hash sftp file", err)
	}
	info, err := client.Lstat(cleanRemote(remotePath))
	if err != nil {
		return FileInfo{}, status.Wrap(status.KindNotFound, "stat sftp file for hash", err)
	}
	return FileInfo{
		Path:      cleanRemote(remotePath),
		Size:      info.Size(),
		IsDir:     info.IsDir(),
		IsSymlink: info.Mode()&os.ModeSymlink != 0,
		SHA256:    sum,
	}, nil
}

func (t *SFTPTransport) ReadDir(ctx context.Context, remotePath string) ([]FileInfo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	client, err := t.connectLocked(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := client.ReadDir(cleanRemote(remotePath))
	if err != nil {
		return nil, status.Wrap(status.KindNotFound, "read sftp dir", err)
	}
	result := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		result = append(result, FileInfo{
			Path:      path.Join(cleanRemote(remotePath), entry.Name()),
			Size:      entry.Size(),
			IsDir:     entry.IsDir(),
			IsSymlink: entry.Mode()&os.ModeSymlink != 0,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Path < result[j].Path
	})
	return result, nil
}

func (t *SFTPTransport) Rename(ctx context.Context, fromPath string, toPath string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	client, err := t.connectLocked(ctx)
	if err != nil {
		return err
	}
	if err := client.MkdirAll(path.Dir(cleanRemote(toPath))); err != nil {
		return status.Wrap(status.KindInternal, "prepare sftp rename target", err)
	}
	if err := client.PosixRename(cleanRemote(fromPath), cleanRemote(toPath)); err != nil {
		if err := client.Rename(cleanRemote(fromPath), cleanRemote(toPath)); err != nil {
			return status.Wrap(status.KindInternal, "rename sftp path", err)
		}
	}
	return nil
}

func (t *SFTPTransport) Mkdir(ctx context.Context, remotePath string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	client, err := t.connectLocked(ctx)
	if err != nil {
		return err
	}
	if err := client.Mkdir(cleanRemote(remotePath)); err != nil {
		return status.Wrap(status.KindInternal, "mkdir sftp path", err)
	}
	return nil
}

func (t *SFTPTransport) MkdirAll(ctx context.Context, remotePath string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	client, err := t.connectLocked(ctx)
	if err != nil {
		return err
	}
	if err := client.MkdirAll(cleanRemote(remotePath)); err != nil {
		return status.Wrap(status.KindInternal, "mkdirall sftp path", err)
	}
	return nil
}

func (t *SFTPTransport) RemoveAll(ctx context.Context, remotePath string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	client, err := t.connectLocked(ctx)
	if err != nil {
		return err
	}
	if err := client.RemoveAll(cleanRemote(remotePath)); err != nil && !isMissingSFTP(err) {
		return status.Wrap(status.KindInternal, "remove sftp path", err)
	}
	return nil
}

func (t *SFTPTransport) Exists(ctx context.Context, remotePath string) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	client, err := t.connectLocked(ctx)
	if err != nil {
		return false, err
	}
	_, err = client.Lstat(cleanRemote(remotePath))
	if err == nil {
		return true, nil
	}
	if isMissingSFTP(err) {
		return false, nil
	}
	return false, status.Wrap(status.KindInternal, "stat sftp path", err)
}

func (t *SFTPTransport) probeFilesystem(ctx context.Context) error {
	probeBase := path.Join(cleanRemote(t.BasePath), ".deploypier", "probe")
	if err := t.MkdirAll(ctx, probeBase); err != nil {
		return err
	}
	stage := path.Join(probeBase, "stage")
	swapped := path.Join(probeBase, "swapped")
	_ = t.RemoveAll(ctx, stage)
	_ = t.RemoveAll(ctx, swapped)
	if err := t.Mkdir(ctx, stage); err != nil {
		return status.Wrap(status.KindInternal, "probe sftp mkdir", err)
	}
	if err := t.Rename(ctx, stage, swapped); err != nil {
		return status.Wrap(status.KindUnsupported, "probe sftp rename", err)
	}
	_ = t.RemoveAll(ctx, swapped)
	return nil
}

func (t *SFTPTransport) connectLocked(ctx context.Context) (*sftp.Client, error) {
	if t.client != nil {
		if _, err := t.client.Getwd(); err == nil {
			return t.client, nil
		}
		_ = t.client.Close()
		_ = t.sshClient.Close()
		t.client = nil
		t.sshClient = nil
	}

	auth, err := t.authMethods()
	if err != nil {
		return nil, err
	}
	callback, err := t.hostKeyCallback()
	if err != nil {
		return nil, err
	}
	sshConfig := &ssh.ClientConfig{
		User:            t.User,
		Auth:            auth,
		HostKeyCallback: callback,
		Timeout:         30 * time.Second,
	}
	address := fmt.Sprintf("%s:%d", t.Host, defaultPort(t.Port, 22))
	sshClient, err := ssh.Dial("tcp", address, sshConfig)
	if err != nil {
		return nil, status.Wrap(status.KindTemporary, "dial sftp transport", err)
	}
	client, err := sftp.NewClient(sshClient)
	if err != nil {
		_ = sshClient.Close()
		return nil, status.Wrap(status.KindTemporary, "create sftp client", err)
	}
	t.sshClient = sshClient
	t.client = client
	return t.client, nil
}

func (t *SFTPTransport) authMethods() ([]ssh.AuthMethod, error) {
	methods := make([]ssh.AuthMethod, 0, 2)
	if strings.TrimSpace(t.Password) != "" {
		methods = append(methods, ssh.Password(t.Password))
	}
	if strings.TrimSpace(t.PrivateKey) != "" {
		signer, err := parsePrivateKey(t.PrivateKey)
		if err != nil {
			return nil, err
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if len(methods) == 0 {
		return nil, status.Wrap(status.KindConfig, "configure sftp auth", errors.New("missing ssh auth method"))
	}
	return methods, nil
}

func (t *SFTPTransport) hostKeyCallback() (ssh.HostKeyCallback, error) {
	if t.Insecure {
		return ssh.InsecureIgnoreHostKey(), nil
	}
	knownHostsPath := t.KnownHostsPath
	if strings.TrimSpace(knownHostsPath) == "" {
		defaultPath, err := defaultKnownHostsPath()
		if err != nil {
			return nil, err
		}
		knownHostsPath = defaultPath
	}
	callback, err := knownhosts.New(knownHostsPath)
	if err != nil {
		return nil, status.Wrap(status.KindConfig, "load known_hosts", err)
	}
	return callback, nil
}

func (t *SFTPTransport) writeRemoteFile(localPath string, remotePath string, mode fs.FileMode) error {
	data, err := os.ReadFile(localPath)
	if err != nil {
		return status.Wrap(status.KindNotFound, "read upload source", err)
	}
	return t.writeBytes(context.Background(), remotePath, data, mode)
}

func (t *SFTPTransport) writeBytes(ctx context.Context, remotePath string, data []byte, modes ...fs.FileMode) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	client, err := t.connectLocked(ctx)
	if err != nil {
		return err
	}
	if err := client.MkdirAll(path.Dir(cleanRemote(remotePath))); err != nil {
		return status.Wrap(status.KindInternal, "prepare sftp upload directory", err)
	}
	file, err := client.Create(cleanRemote(remotePath))
	if err != nil {
		return status.Wrap(status.KindInternal, "create sftp target", err)
	}
	if _, err := io.Copy(file, bytes.NewReader(data)); err != nil {
		file.Close()
		return status.Wrap(status.KindInternal, "write sftp target", err)
	}
	if err := file.Close(); err != nil {
		return status.Wrap(status.KindInternal, "close sftp target", err)
	}
	if len(modes) > 0 && modes[0] != 0 {
		if err := client.Chmod(cleanRemote(remotePath), modes[0]); err != nil {
			return status.Wrap(status.KindInternal, "chmod sftp target", err)
		}
	}
	return nil
}

func parsePrivateKey(value string) (ssh.Signer, error) {
	raw := []byte(value)
	if !strings.Contains(value, "BEGIN") {
		if fileBytes, err := os.ReadFile(value); err == nil {
			raw = fileBytes
		}
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, status.Wrap(status.KindConfig, "parse ssh private key", err)
	}
	return signer, nil
}

func defaultKnownHostsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", status.Wrap(status.KindConfig, "resolve user home for known_hosts", err)
	}
	return filepath.ToSlash(filepath.Join(home, ".ssh", "known_hosts")), nil
}

func isMissingSFTP(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "not exist") || strings.Contains(lower, "no such file")
}
