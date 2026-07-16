package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAppliesDefaultsAndEnvOverrides(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "deploy.yml")

	if err := os.WriteFile(configPath, []byte(`
project:
  name: example
build:
  include:
    - app/**
transport:
  kind: local
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath, map[string]string{
		EnvPrefix + "PROJECT_NAME":       "override-name",
		EnvPrefix + "RELEASE_RETAIN":     "9",
		EnvPrefix + "BUILD_EXCLUDE":      "node_modules/**,.git/**",
		EnvPrefix + "TRANSPORT_PATH":     "./remote-target",
		EnvPrefix + "ACTIVATION_POINTER": "./state/current.txt",
	}, tempDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Project.Name != "override-name" {
		t.Fatalf("unexpected project name: %s", cfg.Project.Name)
	}
	if cfg.Release.Retain != 9 {
		t.Fatalf("unexpected retain value: %d", cfg.Release.Retain)
	}
	if cfg.Release.UploadMode != "files" {
		t.Fatalf("unexpected default upload mode: %s", cfg.Release.UploadMode)
	}
	if len(cfg.Build.Exclude) != 2 {
		t.Fatalf("unexpected exclude count: %d", len(cfg.Build.Exclude))
	}
	expectedTransport := filepath.Join(tempDir, "remote-target")
	if cfg.Transport.Path != expectedTransport {
		t.Fatalf("unexpected transport path: %s", cfg.Transport.Path)
	}
	expectedPointer := filepath.Join(tempDir, "state", "current.txt")
	if cfg.Activation.CurrentPointer != expectedPointer {
		t.Fatalf("unexpected activation pointer: %s", cfg.Activation.CurrentPointer)
	}
	expectedRuntimeAppRoot := filepath.Join(tempDir, ".deploypier", "remote", "app")
	if cfg.Runtime.AppRoot != expectedRuntimeAppRoot {
		t.Fatalf("unexpected runtime app root: %s", cfg.Runtime.AppRoot)
	}
	if cfg.Runtime.CurrentPointer != expectedPointer {
		t.Fatalf("unexpected runtime pointer: %s", cfg.Runtime.CurrentPointer)
	}
}

func TestLoadSupportsDeployEnvNames(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "deploy.yml")

	if err := os.WriteFile(configPath, []byte(`
project:
  name: example
transport:
  kind: ftps
  protocol: ftps
remote:
  app_root: /tmp/app
  public_root: /tmp/public
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := Load(configPath, map[string]string{
		"DEPLOY_HOST":                     "ftp.example.com",
		"DEPLOY_PORT":                     "2121",
		"DEPLOY_USER":                     "deploy-user",
		"DEPLOY_REMOTE_APP_ROOT":          "/home/example/app",
		"DEPLOY_REMOTE_PUBLIC_ROOT":       "/home/example/public_html",
		"DEPLOY_RUNTIME_APP_ROOT":         "/srv/runtime/app",
		"DEPLOY_RUNTIME_CURRENT_POINTER":  "/srv/runtime/.deploypier/current.txt",
		"DEPLOY_HOOK_TIMEOUT":             "12m",
		"DEPLOY_PASSWORD":                 "secret",
		EnvPrefix + "RELEASE_UPLOAD_MODE": "archive",
	}, tempDir)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if cfg.Transport.Host != "ftp.example.com" {
		t.Fatalf("unexpected deploy host: %s", cfg.Transport.Host)
	}
	if cfg.Transport.Port != 2121 {
		t.Fatalf("unexpected deploy port: %d", cfg.Transport.Port)
	}
	if cfg.Transport.User != "deploy-user" {
		t.Fatalf("unexpected deploy user: %s", cfg.Transport.User)
	}
	if cfg.Remote.AppRoot != "/home/example/app" {
		t.Fatalf("unexpected app root: %s", cfg.Remote.AppRoot)
	}
	if cfg.Remote.PublicRoot != "/home/example/public_html" {
		t.Fatalf("unexpected public root: %s", cfg.Remote.PublicRoot)
	}
	if cfg.Transport.Password != "secret" {
		t.Fatalf("expected password to be loaded from env")
	}
	if cfg.Runtime.AppRoot != "/srv/runtime/app" {
		t.Fatalf("unexpected runtime app root: %s", cfg.Runtime.AppRoot)
	}
	if cfg.Runtime.CurrentPointer != "/srv/runtime/.deploypier/current.txt" {
		t.Fatalf("unexpected runtime pointer: %s", cfg.Runtime.CurrentPointer)
	}
	if cfg.PostDeploy.RequestTimeout != "12m" {
		t.Fatalf("unexpected request timeout: %s", cfg.PostDeploy.RequestTimeout)
	}
	if cfg.Release.UploadMode != "archive" {
		t.Fatalf("unexpected upload mode: %s", cfg.Release.UploadMode)
	}
}

func TestValidateRejectsMissingHookCommand(t *testing.T) {
	cfg := defaults(t.TempDir())
	cfg.Hooks.BeforeBuild = []HookSpec{{Name: "bad"}}

	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error")
	}
}

func TestValidateAcceptsBypassPostDeployMode(t *testing.T) {
	cfg := defaults(t.TempDir())
	cfg.PostDeploy.Mode = "bypass"
	normalizeRuntime(&cfg)

	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected bypass mode to be accepted: %v", err)
	}
}

func TestValidateRejectsUnknownRemoteOpsMode(t *testing.T) {
	cfg := defaults(t.TempDir())
	cfg.PostDeploy.RemoteOps = "maybe"
	normalizeRuntime(&cfg)

	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error for remote ops mode")
	}
}

func TestValidateRejectsUnknownUploadMode(t *testing.T) {
	cfg := defaults(t.TempDir())
	cfg.Release.UploadMode = "tarball"
	normalizeRuntime(&cfg)

	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected validation error for upload mode")
	}
}
