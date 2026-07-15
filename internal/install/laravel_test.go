package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInstallCreatesLaravelHookFilesAndSnippets(t *testing.T) {
	projectRoot := t.TempDir()
	seedLaravelProject(t, projectRoot, false)
	mustWrite(t, filepath.Join(projectRoot, ".env.example"), "APP_NAME=Laravel\n")

	installer := NewLaravelHookInstaller()
	installer.Now = func() time.Time {
		return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	}

	created, err := installer.Install(projectRoot, false)
	if err != nil {
		t.Fatalf("install scaffold: %v", err)
	}

	if len(created) < 10 {
		t.Fatalf("expected many created paths, got %d", len(created))
	}

	assertExists(t, filepath.Join(projectRoot, "config", "deploypier.php"))
	assertExists(t, filepath.Join(projectRoot, "app", "Services", "Deploy", "DeployHookReceiverService.php"))
	assertExists(t, filepath.Join(projectRoot, "database", "migrations", "2026_07_14_120000_create_deploy_hook_executions_table.php"))
	assertExists(t, filepath.Join(projectRoot, "routes", "api.php"))

	routeContent := mustRead(t, filepath.Join(projectRoot, "routes", "api.php"))
	if !strings.Contains(routeContent, "internal.deploy.receive") {
		t.Fatalf("expected route snippet to be appended")
	}

	bootstrapContent := mustRead(t, filepath.Join(projectRoot, "bootstrap", "app.php"))
	if !strings.Contains(bootstrapContent, "api: __DIR__.'/../routes/api.php'") {
		t.Fatalf("expected bootstrap/app.php to register routes/api.php")
	}

	envContent := mustRead(t, filepath.Join(projectRoot, ".env.example"))
	if !strings.Contains(envContent, "SYSTEM_DEPLOY_RECEIVER_ENABLED=false") {
		t.Fatalf("expected env snippet to be appended")
	}
}

func TestInstallFailsWhenProjectIsNotLaravel(t *testing.T) {
	projectRoot := t.TempDir()
	installer := NewLaravelHookInstaller()

	if _, err := installer.Install(projectRoot, false); err == nil {
		t.Fatalf("expected validation error")
	}
}

func TestInstallLocawebBootstrapCreatesScripts(t *testing.T) {
	projectRoot := t.TempDir()
	seedLaravelProject(t, projectRoot, false)

	installer := NewLocawebBootstrapInstaller()
	created, err := installer.Install(projectRoot, "myftpuser", false)
	if err != nil {
		t.Fatalf("install locaweb bootstrap: %v", err)
	}

	if len(created) != 3 {
		t.Fatalf("expected 3 created files, got %d", len(created))
	}

	scriptContent := mustRead(t, filepath.Join(projectRoot, "scripts", "locaweb", "bootstrap-first-deploy.sh"))
	if !strings.Contains(scriptContent, `alias composer="/usr/bin/php84 /home/myftpuser/composer.phar"`) {
		t.Fatalf("expected ftp user inside bootstrap script")
	}

	docContent := mustRead(t, filepath.Join(projectRoot, "docs", "locaweb-bootstrap.md"))
	if !strings.Contains(docContent, "storage:link") {
		t.Fatalf("expected docs to mention storage link limitation")
	}
}

func TestInitLocawebCreatesDeployConfigFiles(t *testing.T) {
	projectRoot := t.TempDir()
	seedLaravelProject(t, projectRoot, false)

	initializer := NewLocawebConfigInitializer()
	created, err := initializer.Install(projectRoot, "myftpuser", false)
	if err != nil {
		t.Fatalf("init locaweb config: %v", err)
	}

	if len(created) != 4 {
		t.Fatalf("expected 4 created files, got %d", len(created))
	}

	deployYAML := mustRead(t, filepath.Join(projectRoot, "deploy.yml"))
	if !strings.Contains(deployYAML, `protocol: "ftps"`) {
		t.Fatalf("expected deploy.yml to target ftps")
	}
	if !strings.Contains(deployYAML, `public_root: "/public_html"`) {
		t.Fatalf("expected deploy.yml to contain transport public_html path")
	}
	if !strings.Contains(deployYAML, `runtime:`) || !strings.Contains(deployYAML, `/home/myftpuser/.deploypier/current.txt`) {
		t.Fatalf("expected deploy.yml to contain runtime paths")
	}

	deployEnv := mustRead(t, filepath.Join(projectRoot, ".deploy.env.example"))
	if !strings.Contains(deployEnv, "DEPLOY_USER=myftpuser") {
		t.Fatalf("expected ftp user inside deploy env example")
	}
	if !strings.Contains(deployEnv, "DEPLOY_REMOTE_APP_ROOT=/app") || !strings.Contains(deployEnv, "DEPLOY_RUNTIME_APP_ROOT=/home/myftpuser/app") {
		t.Fatalf("expected deploy env example to separate transport and runtime paths")
	}

	indexExample := mustRead(t, filepath.Join(projectRoot, "docs", "deploypier-public-index.php.example"))
	if !strings.Contains(indexExample, ".deploypier/current.txt") {
		t.Fatalf("expected public index example to use current pointer")
	}
}

func TestInstallReusesExistingAPIRoutesFileAndBootstrapMapping(t *testing.T) {
	projectRoot := t.TempDir()
	seedLaravelProject(t, projectRoot, true)

	installer := NewLaravelHookInstaller()
	installer.Now = func() time.Time {
		return time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	}

	if _, err := installer.Install(projectRoot, false); err != nil {
		t.Fatalf("install scaffold: %v", err)
	}

	bootstrapContent := mustRead(t, filepath.Join(projectRoot, "bootstrap", "app.php"))
	if strings.Count(bootstrapContent, "routes/api.php") != 1 {
		t.Fatalf("expected bootstrap/app.php to keep a single api routing entry")
	}
}

func seedLaravelProject(t *testing.T, projectRoot string, withAPI bool) {
	t.Helper()
	mustWrite(t, filepath.Join(projectRoot, "artisan"), "php")
	mustWrite(t, filepath.Join(projectRoot, "composer.json"), "{}")

	bootstrap := `<?php

use Illuminate\Foundation\Application;
use Illuminate\Foundation\Configuration\Exceptions;
use Illuminate\Foundation\Configuration\Middleware;

return Application::configure(basePath: dirname(__DIR__))
    ->withRouting(
        web: __DIR__.'/../routes/web.php',
        commands: __DIR__.'/../routes/console.php',
        health: '/up',
    )
    ->withMiddleware(function (Middleware $middleware): void {
        //
    })
    ->withExceptions(function (Exceptions $exceptions): void {
        //
    })->create();
`
	if withAPI {
		bootstrap = strings.Replace(bootstrap, "commands: __DIR__.'/../routes/console.php',", "api: __DIR__.'/../routes/api.php',\n        commands: __DIR__.'/../routes/console.php',", 1)
		mustWrite(t, filepath.Join(projectRoot, "routes", "api.php"), "<?php\n\nuse Illuminate\\Support\\Facades\\Route;\n")
	}

	mustWrite(t, filepath.Join(projectRoot, "bootstrap", "app.php"), bootstrap)
}

func mustWrite(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	return string(raw)
}

func assertExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected file to exist: %s", path)
	}
}
