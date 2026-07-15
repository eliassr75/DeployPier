package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/deploypier/deploypier/internal/app"
	"github.com/deploypier/deploypier/internal/status"
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

func TestEmptyDashAndBoolLabel(t *testing.T) {
	if emptyDash("") != "-" {
		t.Fatalf("expected dash for empty value")
	}
	if boolLabel(true) != "ok" {
		t.Fatalf("expected ok label for true")
	}
	if boolLabel(false) != "missing" {
		t.Fatalf("expected missing label for false")
	}
}

func TestDoctorExtraNotesForCreatedPublicIndex(t *testing.T) {
	notes := doctorExtraNotes(app.DoctorCheck{
		Name: "public_index",
		Report: status.Report{
			Code: "created",
		},
	})

	if len(notes) != 3 {
		t.Fatalf("expected 3 notes, got %d", len(notes))
	}
	if !strings.Contains(notes[0], "bootstrap remoto") {
		t.Fatalf("unexpected first note: %s", notes[0])
	}
}

func TestRunDoctorPrintsExtraNotesForCreatedPublicIndex(t *testing.T) {
	tempDir := t.TempDir()
	remoteDir := filepath.Join(tempDir, "remote")

	configContent := `
project:
  name: "demo"
  root: "."
  framework: "laravel"

build:
  php_command: "composer install --no-dev --prefer-dist --optimize-autoloader"
  node_command: "npm ci && npm run build"

release:
  directory: "./.deploypier/releases"
  retain: 5

transport:
  kind: "local"
  protocol: "local"
  path: "` + filepath.ToSlash(remoteDir) + `"

remote:
  app_root: "` + filepath.ToSlash(filepath.Join(remoteDir, "app")) + `"
  public_root: "` + filepath.ToSlash(filepath.Join(remoteDir, "public_html")) + `"
  layout: "release-based"

runtime:
  app_root: "/home/storage/demo/app"
  current_pointer: "/home/storage/demo/.deploypier/current.txt"

activation:
  kind: "pointer"
  current_pointer: "` + filepath.ToSlash(filepath.Join(remoteDir, ".deploypier", "current.txt")) + `"

post_deploy:
  mode: "skip"
`

	configPath := filepath.Join(tempDir, "deploy.yml")
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var stdout bytes.Buffer
	if err := runDoctor(context.Background(), []string{"-config", configPath}, &stdout, nil, tempDir); err != nil {
		t.Fatalf("runDoctor: %v", err)
	}

	output := stdout.String()
	if !strings.Contains(output, "[OK] public_index: remote public index was missing and a DeployPier bootstrap index.php was created") {
		t.Fatalf("unexpected doctor output: %s", output)
	}
	if !strings.Contains(output, "bootstrap remoto criado automaticamente para current-pointer mode") {
		t.Fatalf("expected extra public index note in output: %s", output)
	}
}
