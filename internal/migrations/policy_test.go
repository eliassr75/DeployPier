package migrations

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAssessBlocksAutoWhenGitDiffIsUnavailable(t *testing.T) {
	projectRoot := t.TempDir()

	assessment, err := Assess(context.Background(), projectRoot)
	if err != nil {
		t.Fatalf("assess migrations: %v", err)
	}

	if assessment.AutoAllowed {
		t.Fatalf("expected auto migration to be blocked when git diff is unavailable")
	}
	if !assessment.RequiresManual {
		t.Fatalf("expected manual review requirement when git diff is unavailable")
	}
	if len(assessment.Blocking) == 0 {
		t.Fatalf("expected blocking reason when git diff is unavailable")
	}
}

func TestAutoSafeAllowlistAcceptsSimpleCreateTable(t *testing.T) {
	content := `<?php

use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class {
    public function up(): void
    {
        Schema::create('widgets', function (Blueprint $table) {
            $table->id();
            $table->string('name');
            $table->timestamps();
        });
    }
};
`

	if !autoSafe(content) {
		t.Fatalf("expected simple create table migration to be auto safe")
	}
}

func TestAutoSafeRejectsTableAlterations(t *testing.T) {
	content := `<?php

use Illuminate\Database\Schema\Blueprint;
use Illuminate\Support\Facades\Schema;

return new class {
    public function up(): void
    {
        Schema::table('widgets', function (Blueprint $table) {
            $table->string('code')->index();
        });
    }
};
`

	if autoSafe(content) {
		t.Fatalf("expected table alteration migration to be blocked")
	}
}

func TestChangedMigrationFilesParsesGitDiffOutput(t *testing.T) {
	projectRoot := t.TempDir()
	writeGitStub(t, projectRoot, "database/migrations/2026_07_15_000000_create_widgets_table.php\n")

	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", projectRoot+string(os.PathListSeparator)+originalPath)

	files, err := changedMigrationFiles(context.Background(), projectRoot)
	if err != nil {
		t.Fatalf("changed migration files: %v", err)
	}

	if len(files) != 1 || files[0] != "database/migrations/2026_07_15_000000_create_widgets_table.php" {
		t.Fatalf("unexpected migration files: %#v", files)
	}
}

func writeGitStub(t *testing.T, dir string, stdout string) {
	t.Helper()

	if runtime.GOOS == "windows" {
		gitScript := filepath.Join(dir, "git.cmd")
		content := "@echo off\r\n" + "echo " + stdout
		if err := os.WriteFile(gitScript, []byte(content), 0o644); err != nil {
			t.Fatalf("write git stub: %v", err)
		}
		return
	}

	gitScript := filepath.Join(dir, "git")
	content := "#!/bin/sh\nprintf '%s' \"" + stdout + "\"\n"
	if err := os.WriteFile(gitScript, []byte(content), 0o755); err != nil {
		t.Fatalf("write git stub: %v", err)
	}
}
