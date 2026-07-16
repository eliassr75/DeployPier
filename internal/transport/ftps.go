package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/deploypier/deploypier/internal/build"
	"github.com/deploypier/deploypier/internal/config"
	"github.com/deploypier/deploypier/internal/status"
	"github.com/jlaffaye/ftp"
)

type FTPSTransport struct {
	Host     string
	Port     int
	User     string
	Password string
	BasePath string
	Insecure bool
	UseTLS   bool

	mu   sync.Mutex
	conn *ftp.ServerConn
}

func NewFTPSTransport(cfg config.TransportConfig) *FTPSTransport {
	kind := normalizedKind(cfg)
	return &FTPSTransport{
		Host:     cfg.Host,
		Port:     cfg.Port,
		User:     cfg.User,
		Password: cfg.Password,
		BasePath: cfg.Path,
		Insecure: cfg.AllowInsecure,
		UseTLS:   kind != "ftp",
	}
}

func (t *FTPSTransport) Name() string {
	if !t.UseTLS {
		return "ftp"
	}
	if t.Insecure {
		return "ftps-insecure"
	}
	return "ftps"
}

func (t *FTPSTransport) Probe(ctx context.Context) status.Report {
	if !t.UseTLS && t.Insecure {
		if err := t.probeFilesystem(ctx); err != nil {
			return status.Classify(err)
		}
		return status.Report{
			Level:   status.LevelWarn,
			Code:    "insecure_ftp",
			Message: "plain FTP is configured with explicit opt-in",
		}
	}
	if err := t.probeFilesystem(ctx); err != nil {
		return status.Classify(err)
	}
	if t.Insecure {
		return status.Report{
			Level:   status.LevelWarn,
			Code:    "insecure_ftps",
			Message: fmt.Sprintf("%s is connected with certificate verification disabled", t.logProtocolName()),
		}
	}
	return status.Report{
		Level:   status.LevelOK,
		Code:    "ok",
		Message: fmt.Sprintf("%s transport ready", t.logProtocolName()),
	}
}

func (t *FTPSTransport) Inspect(ctx context.Context) (Inspection, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	conn, err := t.connectLocked(ctx)
	if err != nil {
		return Inspection{}, err
	}

	currentDir, err := conn.CurrentDir()
	if err != nil {
		return Inspection{}, status.Wrap(status.KindInternal, "inspect "+t.logProtocolName()+" current dir", err)
	}

	return Inspection{
		CurrentDir:   cleanRemote(currentDir),
		ResolvedPath: cleanRemote(t.BasePath),
	}, nil
}

func (t *FTPSTransport) UploadRelease(ctx context.Context, release build.Release, remotePath string, progress UploadProgressFunc) (UploadResult, error) {
	if exists, err := t.Exists(ctx, remotePath); err != nil {
		return UploadResult{}, err
	} else if exists && !release.AllowExistingRemote {
		return UploadResult{}, status.Wrap(status.KindConflict, "upload "+t.logProtocolName()+" release", errors.New("release already exists remotely"))
	} else if !exists {
		if err := t.MkdirAll(ctx, remotePath); err != nil {
			return UploadResult{}, err
		}
	}
	if strings.TrimSpace(release.ArchivePath) != "" {
		if err := uploadArchiveRelease(ctx, release, remotePath, t.writeRemoteFile, progress); err != nil {
			return UploadResult{}, err
		}
		return UploadResult{
			RemotePath:   remotePath,
			ManifestPath: path.Join(remotePath, "manifest.json"),
		}, nil
	}
	if err := uploadReleaseTree(ctx, release, remotePath, t.writeRemoteFile, progress); err != nil {
		return UploadResult{}, err
	}
	return UploadResult{
		RemotePath:   remotePath,
		ManifestPath: path.Join(remotePath, "manifest.json"),
	}, nil
}

func (t *FTPSTransport) ReadFile(ctx context.Context, remotePath string) ([]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	conn, err := t.connectLocked(ctx)
	if err != nil {
		return nil, err
	}
	reader, err := conn.Retr(cleanRemote(remotePath))
	if err != nil {
		return nil, status.Wrap(status.KindNotFound, "retrieve "+t.logProtocolName()+" file", err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, status.Wrap(status.KindInternal, "read "+t.logProtocolName()+" file", err)
	}
	return data, nil
}

func (t *FTPSTransport) WriteFile(ctx context.Context, remotePath string, data []byte) error {
	return t.writeBytes(ctx, remotePath, data)
}

func (t *FTPSTransport) Stat(ctx context.Context, remotePath string) (FileInfo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	conn, err := t.connectLocked(ctx)
	if err != nil {
		return FileInfo{}, err
	}
	entry, err := conn.GetEntry(cleanRemote(remotePath))
	if err != nil {
		return FileInfo{}, status.Wrap(status.KindNotFound, "stat "+t.logProtocolName()+" path", err)
	}
	return FileInfo{
		Path:  cleanRemote(remotePath),
		Size:  int64(entry.Size),
		IsDir: entry.Type == ftp.EntryTypeFolder,
	}, nil
}

func (t *FTPSTransport) HashFile(ctx context.Context, remotePath string) (FileInfo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	conn, err := t.connectLocked(ctx)
	if err != nil {
		return FileInfo{}, err
	}
	reader, err := conn.Retr(cleanRemote(remotePath))
	if err != nil {
		return FileInfo{}, status.Wrap(status.KindNotFound, "retrieve "+t.logProtocolName()+" file for hash", err)
	}
	defer reader.Close()

	sum, err := hashStream(reader)
	if err != nil {
		return FileInfo{}, status.Wrap(status.KindInternal, "hash "+t.logProtocolName()+" file", err)
	}
	entry, err := conn.GetEntry(cleanRemote(remotePath))
	if err != nil {
		return FileInfo{}, status.Wrap(status.KindNotFound, "stat "+t.logProtocolName()+" file for hash", err)
	}
	return FileInfo{
		Path:   cleanRemote(remotePath),
		Size:   int64(entry.Size),
		IsDir:  entry.Type == ftp.EntryTypeFolder,
		SHA256: sum,
	}, nil
}

func (t *FTPSTransport) ReadDir(ctx context.Context, remotePath string) ([]FileInfo, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	conn, err := t.connectLocked(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := conn.List(cleanRemote(remotePath))
	if err != nil {
		return nil, status.Wrap(status.KindNotFound, "list "+t.logProtocolName()+" dir", err)
	}
	result := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		result = append(result, FileInfo{
			Path:  path.Join(cleanRemote(remotePath), entry.Name),
			Size:  int64(entry.Size),
			IsDir: entry.Type == ftp.EntryTypeFolder,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Path < result[j].Path
	})
	return result, nil
}

func (t *FTPSTransport) Rename(ctx context.Context, fromPath string, toPath string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	conn, err := t.connectLocked(ctx)
	if err != nil {
		return err
	}
	if err := t.mkdirAllLocked(conn, path.Dir(cleanRemote(toPath))); err != nil {
		return err
	}
	if err := conn.Rename(cleanRemote(fromPath), cleanRemote(toPath)); err != nil {
		return status.Wrap(status.KindInternal, "rename "+t.logProtocolName()+" path", err)
	}
	return nil
}

func (t *FTPSTransport) Mkdir(ctx context.Context, remotePath string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	conn, err := t.connectLocked(ctx)
	if err != nil {
		return err
	}
	if err := conn.MakeDir(cleanRemote(remotePath)); err != nil {
		return status.Wrap(status.KindInternal, "mkdir "+t.logProtocolName()+" path", err)
	}
	return nil
}

func (t *FTPSTransport) MkdirAll(ctx context.Context, remotePath string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	conn, err := t.connectLocked(ctx)
	if err != nil {
		return err
	}
	return t.mkdirAllLocked(conn, cleanRemote(remotePath))
}

func (t *FTPSTransport) RemoveAll(ctx context.Context, remotePath string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	conn, err := t.connectLocked(ctx)
	if err != nil {
		return err
	}
	target := cleanRemote(remotePath)
	entry, statErr := conn.GetEntry(target)
	if statErr != nil {
		return nil
	}
	if entry.Type == ftp.EntryTypeFolder {
		if err := conn.RemoveDirRecur(target); err != nil {
			if isMissingFTP(err) {
				return nil
			}
			return status.Wrap(status.KindInternal, "remove "+t.logProtocolName()+" dir", err)
		}
		return nil
	}
	if err := conn.Delete(target); err != nil {
		if isMissingFTP(err) {
			return nil
		}
		return status.Wrap(status.KindInternal, "delete "+t.logProtocolName()+" file", err)
	}
	return nil
}

func (t *FTPSTransport) Exists(ctx context.Context, remotePath string) (bool, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	conn, err := t.connectLocked(ctx)
	if err != nil {
		return false, err
	}
	_, err = conn.GetEntry(cleanRemote(remotePath))
	if err == nil {
		return true, nil
	}
	if isMissingFTP(err) {
		return false, nil
	}
	return false, status.Wrap(status.KindInternal, "stat "+t.logProtocolName()+" path", err)
}

func (t *FTPSTransport) probeFilesystem(ctx context.Context) error {
	probeBase := path.Join(cleanRemote(t.BasePath), ".deploypier", "probe")
	if err := t.MkdirAll(ctx, probeBase); err != nil {
		return err
	}
	stage := path.Join(probeBase, "stage")
	swapped := path.Join(probeBase, "swapped")
	_ = t.RemoveAll(ctx, stage)
	_ = t.RemoveAll(ctx, swapped)
	if err := t.Mkdir(ctx, stage); err != nil {
		return status.Wrap(status.KindInternal, "probe "+t.logProtocolName()+" mkdir", err)
	}
	if err := t.Rename(ctx, stage, swapped); err != nil {
		return status.Wrap(status.KindUnsupported, "probe "+t.logProtocolName()+" rename", err)
	}
	_ = t.RemoveAll(ctx, swapped)
	return nil
}

func (t *FTPSTransport) connectLocked(ctx context.Context) (*ftp.ServerConn, error) {
	if t.conn != nil {
		if err := t.conn.NoOp(); err == nil {
			return t.conn, nil
		}
		_ = t.conn.Quit()
		t.conn = nil
	}

	address := fmt.Sprintf("%s:%d", t.Host, defaultPort(t.Port, 21))
	options := []ftp.DialOption{
		ftp.DialWithContext(ctx),
		ftp.DialWithTimeout(30 * time.Second),
		ftp.DialWithDisabledEPSV(false),
		ftp.DialWithForceListHidden(true),
	}
	if t.UseTLS {
		tlsConfig := &tls.Config{
			ServerName:         t.Host,
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: t.Insecure,
		}
		if defaultPort(t.Port, 21) == 990 {
			options = append(options, ftp.DialWithTLS(tlsConfig))
		} else {
			options = append(options, ftp.DialWithExplicitTLS(tlsConfig))
		}
	}

	conn, err := ftp.Dial(address, options...)
	if err != nil {
		return nil, status.Wrap(status.KindTemporary, "dial "+t.logProtocolName()+" transport", err)
	}
	if err := conn.Login(t.User, t.Password); err != nil {
		_ = conn.Quit()
		return nil, status.Wrap(status.KindConfig, "login "+t.logProtocolName()+" transport", err)
	}
	if err := conn.Type(ftp.TransferTypeBinary); err != nil {
		_ = conn.Quit()
		return nil, status.Wrap(status.KindInternal, "set "+t.logProtocolName()+" binary mode", err)
	}
	t.conn = conn
	return t.conn, nil
}

func (t *FTPSTransport) mkdirAllLocked(conn *ftp.ServerConn, remotePath string) error {
	cleaned := cleanRemote(remotePath)
	if cleaned == "" || cleaned == "." || cleaned == "/" {
		return nil
	}
	segments := strings.Split(strings.TrimPrefix(cleaned, "/"), "/")
	current := ""
	if strings.HasPrefix(cleaned, "/") {
		current = "/"
	}
	for _, segment := range segments {
		if strings.TrimSpace(segment) == "" {
			continue
		}
		current = path.Join(current, segment)
		if _, err := conn.GetEntry(current); err == nil {
			continue
		}
		if err := conn.MakeDir(current); err != nil {
			if _, statErr := conn.GetEntry(current); statErr == nil {
				continue
			}
			return status.Wrap(status.KindInternal, "mkdirall "+t.logProtocolName()+" path", err)
		}
	}
	return nil
}

func (t *FTPSTransport) writeRemoteFile(localPath string, remotePath string, _ fs.FileMode) error {
	data, err := osReadFile(localPath)
	if err != nil {
		return status.Wrap(status.KindNotFound, "read upload source", err)
	}
	return t.writeBytes(context.Background(), remotePath, data)
}

func (t *FTPSTransport) writeBytes(ctx context.Context, remotePath string, data []byte) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	conn, err := t.connectLocked(ctx)
	if err != nil {
		return err
	}
	if err := t.mkdirAllLocked(conn, path.Dir(cleanRemote(remotePath))); err != nil {
		return err
	}
	if err := conn.Stor(cleanRemote(remotePath), bytes.NewReader(data)); err != nil {
		return status.Wrap(status.KindInternal, "store "+t.logProtocolName()+" file", err)
	}
	return nil
}

func (t *FTPSTransport) logProtocolName() string {
	if !t.UseTLS {
		return "ftp"
	}
	return "ftps"
}

func cleanRemote(value string) string {
	cleaned := path.Clean(strings.ReplaceAll(value, `\`, `/`))
	if cleaned == "." {
		return "/"
	}
	return cleaned
}

func defaultPort(port int, fallback int) int {
	if port > 0 {
		return port
	}
	return fallback
}

func isMissingFTP(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "file unavailable") ||
		strings.Contains(message, "not found") ||
		strings.Contains(message, "no such file or directory") ||
		strings.Contains(message, "550")
}

func osReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}
