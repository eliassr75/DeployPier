package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveEnvLoadsDefaultDeployEnvFile(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "deploy.yml")
	envPath := filepath.Join(tempDir, ".deploy.env")

	if err := os.WriteFile(configPath, []byte("project:\n  name: example\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.WriteFile(envPath, []byte("DEPLOY_HOST=ftp.example.com\nDEPLOY_PORT=2121\n"), 0o644); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	values, err := resolveEnv(configPath, "", []string{"PATH=test"}, tempDir)
	if err != nil {
		t.Fatalf("resolve env: %v", err)
	}

	if values["DEPLOY_HOST"] != "ftp.example.com" {
		t.Fatalf("unexpected DEPLOY_HOST: %s", values["DEPLOY_HOST"])
	}
	if values["DEPLOY_PORT"] != "2121" {
		t.Fatalf("unexpected DEPLOY_PORT: %s", values["DEPLOY_PORT"])
	}
}
