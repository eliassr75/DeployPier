package migrations

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/deploypier/deploypier/internal/status"
)

type Assessment struct {
	AutoAllowed    bool
	HasMigrations  bool
	RequiresManual bool
	Blocking       []string
	Warnings       []string
}

var disallowedMarkers = []string{
	"DB::",
	"Schema::table(",
	"Schema::drop(",
	"Schema::dropIfExists(",
	"DB::table(",
	"->change(",
	"renameColumn(",
	"dropColumn(",
	"dropTable(",
	"dropIndex(",
	"dropForeign(",
	"->unique(",
	"->foreign(",
	"->fullText(",
	"->spatialIndex(",
	"->index(",
	"->constrained(",
	"->json(",
	"->jsonb(",
	"->enum(",
	"->set(",
	"generatedAs(",
	"storedAs(",
	"virtualAs(",
	"useCurrentOnUpdate(",
}

func Assess(ctx context.Context, projectRoot string) (Assessment, error) {
	files, err := changedMigrationFiles(ctx, projectRoot)
	if err != nil {
		return Assessment{
			AutoAllowed:    false,
			RequiresManual: true,
			Blocking: []string{
				"migration diff not available; automatic post-deploy blocked",
			},
		}, nil
	}

	assessment := Assessment{AutoAllowed: true}
	for _, file := range files {
		assessment.HasMigrations = true
		assessment.RequiresManual = true
		raw, err := os.ReadFile(filepath.Join(projectRoot, file))
		if err != nil {
			return Assessment{}, status.Wrap(status.KindInternal, "read migration file", err)
		}
		content := string(raw)
		if autoSafe(content) {
			continue
		}
		assessment.AutoAllowed = false
		assessment.Blocking = append(assessment.Blocking, file+": outside v1 auto-migration allowlist")
	}

	return assessment, nil
}

func autoSafe(content string) bool {
	if !strings.Contains(content, "Schema::create(") {
		return false
	}
	for _, marker := range disallowedMarkers {
		if strings.Contains(content, marker) {
			return false
		}
	}
	return true
}

func changedMigrationFiles(ctx context.Context, projectRoot string) ([]string, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/c", "git diff --name-only HEAD~1 HEAD -- database/migrations")
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-lc", "git diff --name-only HEAD~1 HEAD -- database/migrations")
	}
	cmd.Dir = projectRoot
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	files := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && strings.HasSuffix(trimmed, ".php") {
			files = append(files, filepath.ToSlash(trimmed))
		}
	}

	return files, nil
}
