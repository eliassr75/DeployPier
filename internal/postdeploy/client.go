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
	"strings"
	"time"

	"github.com/deploypier/deploypier/internal/build"
	"github.com/deploypier/deploypier/internal/config"
	"github.com/deploypier/deploypier/internal/status"
)

type Client struct {
	httpClient *http.Client
	now        func() time.Time
}

type Payload struct {
	Operation   string                 `json:"operation"`
	Environment string                 `json:"environment,omitempty"`
	App         string                 `json:"app,omitempty"`
	Mode        string                 `json:"mode,omitempty"`
	ReleaseID   string                 `json:"release_id"`
	Ref         string                 `json:"ref,omitempty"`
	Commit      string                 `json:"commit,omitempty"`
	TriggeredAt string                 `json:"triggered_at"`
	Artifact    Artifact               `json:"artifact"`
	Meta        map[string]interface{} `json:"meta,omitempty"`
}

type Artifact struct {
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	UploadedPath string `json:"uploaded_path,omitempty"`
}

type Result struct {
	StatusCode int
	Body       []byte
}

func NewClient() *Client {
	return &Client{
		httpClient: &http.Client{Timeout: 3 * time.Minute},
		now:        time.Now,
	}
}

func (c *Client) Call(ctx context.Context, cfg config.Config, release build.Release, remotePath string, ref string, commit string) (Result, error) {
	if strings.TrimSpace(cfg.PostDeploy.HookURL) == "" {
		return Result{}, status.Wrap(status.KindConfig, "post-deploy hook", fmt.Errorf("resolved hook URL is empty"))
	}
	if strings.TrimSpace(cfg.PostDeploy.KeyID) == "" || strings.TrimSpace(cfg.PostDeploy.Secret) == "" {
		return Result{}, status.Wrap(status.KindConfig, "post-deploy hook", fmt.Errorf("resolved hook credentials are incomplete"))
	}

	payload := Payload{
		Operation:   "post_deploy_v1",
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
			"tool_version": "0.1.0",
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return Result{}, status.Wrap(status.KindInternal, "marshal post-deploy payload", err)
	}

	timestamp := fmt.Sprintf("%d", c.now().UTC().Unix())
	nonce, err := randomNonce()
	if err != nil {
		return Result{}, status.Wrap(status.KindInternal, "generate post-deploy nonce", err)
	}
	idempotencyKey := fmt.Sprintf("%s-%s", release.ID, nonce)
	signature := sign(cfg.PostDeploy.KeyID, cfg.PostDeploy.Secret, timestamp, nonce, idempotencyKey, body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.PostDeploy.HookURL, bytes.NewReader(body))
	if err != nil {
		return Result{}, status.Wrap(status.KindInternal, "create post-deploy request", err)
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
		return Result{}, status.Wrap(status.KindTemporary, "execute post-deploy request", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, status.Wrap(status.KindInternal, "read post-deploy response", err)
	}

	if resp.StatusCode >= 400 {
		return Result{StatusCode: resp.StatusCode, Body: respBody}, status.Wrap(status.KindConflict, "post-deploy response", fmt.Errorf("received %d", resp.StatusCode))
	}

	return Result{StatusCode: resp.StatusCode, Body: respBody}, nil
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
