package postdeploy

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/deploypier/deploypier/internal/build"
	"github.com/deploypier/deploypier/internal/config"
	"github.com/deploypier/deploypier/internal/status"
	"github.com/deploypier/deploypier/internal/version"
)

const (
	OperationPostDeploy     = "post_deploy_v1"
	OperationPrepareRelease = "prepare_release_v1"
	OperationExtractRelease = "extract_release_v1"
	defaultRequestTimeout   = 3 * time.Minute
)

type Client struct {
	httpClient *http.Client
	now        func() time.Time
}

type Payload struct {
	Operation     string                 `json:"operation"`
	Environment   string                 `json:"environment,omitempty"`
	App           string                 `json:"app,omitempty"`
	Mode          string                 `json:"mode,omitempty"`
	ReleaseID     string                 `json:"release_id"`
	BaseReleaseID string                 `json:"base_release_id,omitempty"`
	Ref           string                 `json:"ref,omitempty"`
	Commit        string                 `json:"commit,omitempty"`
	TriggeredAt   string                 `json:"triggered_at"`
	Artifact      Artifact               `json:"artifact,omitempty"`
	RemovePaths   []string               `json:"remove_paths,omitempty"`
	ChangedPaths  []string               `json:"changed_paths,omitempty"`
	Meta          map[string]interface{} `json:"meta,omitempty"`
}

type Artifact struct {
	SHA256        string `json:"sha256,omitempty"`
	Size          int64  `json:"size,omitempty"`
	UploadedPath  string `json:"uploaded_path,omitempty"`
	ArchiveSHA256 string `json:"archive_sha256,omitempty"`
	ArchiveSize   int64  `json:"archive_size,omitempty"`
}

type PrepareReleaseInput struct {
	Release       build.Release
	BaseReleaseID string
	RemotePath    string
	ChangedPaths  []string
	RemovedPaths  []string
	Ref           string
	Commit        string
}

type ExtractReleaseInput struct {
	Release    build.Release
	RemotePath string
	Ref        string
	Commit     string
}

type Result struct {
	StatusCode int
	Body       []byte
}

func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{},
		now:        time.Now,
	}
}

func (c *Client) Call(ctx context.Context, cfg config.Config, release build.Release, remotePath string, ref string, commit string) (Result, error) {
	return c.call(ctx, cfg, Payload{
		Operation:   OperationPostDeploy,
		Environment: "production",
		App:         cfg.Project.Name,
		Mode:        cfg.Remote.Layout,
		ReleaseID:   release.ID,
		Ref:         ref,
		Commit:      commit,
		TriggeredAt: c.now().UTC().Format(time.RFC3339),
		Artifact: Artifact{
			SHA256:       release.Manifest.SHA256,
			Size:         manifestSize(release.Manifest.Files),
			UploadedPath: remotePath,
		},
		Meta: map[string]interface{}{
			"tool":         "deploypier",
			"tool_version": version.String,
		},
	})
}

func (c *Client) PrepareRelease(ctx context.Context, cfg config.Config, input PrepareReleaseInput) (Result, error) {
	return c.call(ctx, cfg, Payload{
		Operation:     OperationPrepareRelease,
		Environment:   "production",
		App:           cfg.Project.Name,
		Mode:          cfg.Remote.Layout,
		ReleaseID:     input.Release.ID,
		BaseReleaseID: input.BaseReleaseID,
		Ref:           input.Ref,
		Commit:        input.Commit,
		TriggeredAt:   c.now().UTC().Format(time.RFC3339),
		Artifact: Artifact{
			SHA256:       input.Release.Manifest.SHA256,
			Size:         manifestSize(input.Release.Manifest.Files),
			UploadedPath: input.RemotePath,
		},
		RemovePaths:  append([]string{}, input.RemovedPaths...),
		ChangedPaths: append([]string{}, input.ChangedPaths...),
		Meta: map[string]interface{}{
			"tool":         "deploypier",
			"tool_version": version.String,
		},
	})
}

func (c *Client) ExtractRelease(ctx context.Context, cfg config.Config, input ExtractReleaseInput) (Result, error) {
	archive, err := archiveInfo(input.Release.ArchivePath)
	if err != nil {
		return Result{}, err
	}

	return c.call(ctx, cfg, Payload{
		Operation:   OperationExtractRelease,
		Environment: "production",
		App:         cfg.Project.Name,
		Mode:        cfg.Remote.Layout,
		ReleaseID:   input.Release.ID,
		Ref:         input.Ref,
		Commit:      input.Commit,
		TriggeredAt: c.now().UTC().Format(time.RFC3339),
		Artifact: Artifact{
			SHA256:        input.Release.Manifest.SHA256,
			Size:          manifestSize(input.Release.Manifest.Files),
			UploadedPath:  input.RemotePath,
			ArchiveSHA256: archive.SHA256,
			ArchiveSize:   archive.Size,
		},
		Meta: map[string]interface{}{
			"tool":         "deploypier",
			"tool_version": version.String,
		},
	})
}

func (c *Client) call(ctx context.Context, cfg config.Config, payload Payload) (Result, error) {
	if strings.TrimSpace(cfg.PostDeploy.HookURL) == "" {
		return Result{}, status.Wrap(status.KindConfig, "deploy hook", fmt.Errorf("resolved hook URL is empty"))
	}
	if strings.TrimSpace(cfg.PostDeploy.KeyID) == "" || strings.TrimSpace(cfg.PostDeploy.Secret) == "" {
		return Result{}, status.Wrap(status.KindConfig, "deploy hook", fmt.Errorf("resolved hook credentials are incomplete"))
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, status.Wrap(status.KindInternal, "marshal deploy hook payload", err)
	}

	timestamp := fmt.Sprintf("%d", c.now().UTC().Unix())
	nonce, err := randomNonce()
	if err != nil {
		return Result{}, status.Wrap(status.KindInternal, "generate deploy hook nonce", err)
	}
	idempotencyKey := fmt.Sprintf("%s-%s-%s", payload.Operation, payload.ReleaseID, nonce)
	signature := sign(cfg.PostDeploy.KeyID, cfg.PostDeploy.Secret, timestamp, nonce, idempotencyKey, body)

	requestCtx, cancel := c.requestContext(ctx, cfg)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, cfg.PostDeploy.HookURL, bytes.NewReader(body))
	if err != nil {
		return Result{}, status.Wrap(status.KindInternal, "create deploy hook request", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)
	req.Header.Set("X-Deploy-Key-Id", cfg.PostDeploy.KeyID)
	req.Header.Set("X-Deploy-Timestamp", timestamp)
	req.Header.Set("X-Deploy-Nonce", nonce)
	req.Header.Set("X-Deploy-Signature-Version", "v1")
	req.Header.Set("X-Deploy-Signature-Scope", "deploy:post-deploy")
	req.Header.Set("X-Deploy-Signature", signature)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Result{}, status.Wrap(status.KindTemporary, "execute deploy hook request", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, status.Wrap(status.KindInternal, "read deploy hook response", err)
	}

	if resp.StatusCode >= 400 {
		return Result{StatusCode: resp.StatusCode, Body: respBody}, status.Wrap(status.KindConflict, "deploy hook response", fmt.Errorf("received %d", resp.StatusCode))
	}

	return Result{StatusCode: resp.StatusCode, Body: respBody}, nil
}

func (c *Client) requestContext(ctx context.Context, cfg config.Config) (context.Context, context.CancelFunc) {
	timeout := defaultRequestTimeout
	if value := strings.TrimSpace(cfg.PostDeploy.RequestTimeout); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed > 0 {
			timeout = parsed
		}
	}
	return context.WithTimeout(ctx, timeout)
}

func sign(keyID string, secret string, timestamp string, nonce string, idempotencyKey string, body []byte) string {
	bodyHash := sha256.Sum256(body)
	base := strings.Join([]string{
		"v1",
		"deploy:post-deploy",
		keyID,
		timestamp,
		nonce,
		idempotencyKey,
		hex.EncodeToString(bodyHash[:]),
	}, ".")
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(base))
	return hex.EncodeToString(mac.Sum(nil))
}

func manifestSize(files []build.ManifestFile) int64 {
	var total int64
	for _, file := range files {
		total += file.Size
	}
	return total
}

func randomNonce() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

type archiveMetadata struct {
	Size   int64
	SHA256 string
}

func archiveInfo(archivePath string) (archiveMetadata, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return archiveMetadata{}, status.Wrap(status.KindNotFound, "open release archive", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return archiveMetadata{}, status.Wrap(status.KindInternal, "stat release archive", err)
	}

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return archiveMetadata{}, status.Wrap(status.KindInternal, "hash release archive", err)
	}

	return archiveMetadata{
		Size:   info.Size(),
		SHA256: hex.EncodeToString(hasher.Sum(nil)),
	}, nil
}
