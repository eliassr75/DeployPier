package hooks

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/deploypier/deploypier/internal/config"
)

func TestExecRunnerRunsHookAndInjectsMetadata(t *testing.T) {
	runner := NewRunner()
	hooks := []config.HookSpec{{
		Name:    "helper",
		Command: []string{os.Args[0], "-test.run=TestHookHelperProcess", "--", "print-meta"},
		Env: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
		},
	}}

	results, err := runner.RunPhase(context.Background(), "before_build", hooks, Metadata{
		"release_id": "rel-001",
	})
	if err != nil {
		t.Fatalf("run phase: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 hook result, got %d", len(results))
	}
	if !strings.Contains(results[0].Stdout, "rel-001") {
		t.Fatalf("expected metadata in stdout, got %q", results[0].Stdout)
	}
}

func TestExecRunnerAllowsOptionalFailure(t *testing.T) {
	runner := NewRunner()
	hooks := []config.HookSpec{{
		Name:     "optional-failure",
		Optional: true,
		Command:  []string{os.Args[0], "-test.run=TestHookHelperProcess", "--", "exit-3"},
		Env: map[string]string{
			"GO_WANT_HELPER_PROCESS": "1",
		},
	}}

	results, err := runner.RunPhase(context.Background(), "after_push", hooks, nil)
	if err != nil {
		t.Fatalf("expected optional hook to pass, got %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 hook result, got %d", len(results))
	}
	if results[0].ExitCode != 3 {
		t.Fatalf("expected exit code 3, got %d", results[0].ExitCode)
	}
}

func TestHookHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	args := os.Args
	for index, arg := range args {
		if arg == "--" && index+1 < len(args) {
			switch args[index+1] {
			case "print-meta":
				fmt.Fprint(os.Stdout, os.Getenv("DEPLOYPIER_META_RELEASE_ID"))
				os.Exit(0)
			case "exit-3":
				os.Exit(3)
			}
		}
	}
	os.Exit(2)
}
