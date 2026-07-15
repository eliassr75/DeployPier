package hooks

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/deploypier/deploypier/internal/config"
	"github.com/deploypier/deploypier/internal/status"
)

type Metadata map[string]string

type Result struct {
	Name      string
	Phase     string
	Command   []string
	ExitCode  int
	Optional  bool
	Duration  time.Duration
	Stdout    string
	Stderr    string
	Completed bool
}

type Runner interface {
	RunPhase(ctx context.Context, phase string, hooks []config.HookSpec, metadata Metadata) ([]Result, error)
}

type ExecRunner struct{}

func NewRunner() *ExecRunner {
	return &ExecRunner{}
}

func (r *ExecRunner) RunPhase(ctx context.Context, phase string, hooks []config.HookSpec, metadata Metadata) ([]Result, error) {
	results := make([]Result, 0, len(hooks))
	for _, hook := range hooks {
		result, err := r.runOne(ctx, phase, hook, metadata)
		results = append(results, result)
		if err != nil {
			if hook.Optional {
				continue
			}
			return results, err
		}
	}
	return results, nil
}

func (r *ExecRunner) runOne(ctx context.Context, phase string, hook config.HookSpec, metadata Metadata) (Result, error) {
	startedAt := time.Now()
	result := Result{
		Name:     hook.Name,
		Phase:    phase,
		Command:  append([]string{}, hook.Command...),
		Optional: hook.Optional,
		ExitCode: -1,
	}

	runCtx := ctx
	var cancel context.CancelFunc
	if hook.Timeout != "" {
		timeout, err := time.ParseDuration(hook.Timeout)
		if err != nil {
			return result, status.Wrap(status.KindConfig, "parse hook timeout", err)
		}
		runCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(runCtx, hook.Command[0], hook.Command[1:]...)
	cmd.Env = buildEnv(hook.Env, metadata)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	result.Duration = time.Since(startedAt)
	result.Completed = err == nil

	if err == nil {
		result.ExitCode = 0
		return result, nil
	}

	var exitError *exec.ExitError
	if ok := AsExitError(err, &exitError); ok {
		result.ExitCode = exitError.ExitCode()
	} else if runCtx.Err() != nil {
		result.ExitCode = -1
	}

	hookName := hook.Name
	if hookName == "" {
		hookName = strings.Join(hook.Command, " ")
	}
	message := fmt.Sprintf("hook %s failed during %s", hookName, phase)
	if hook.Optional {
		return result, nil
	}
	return result, status.Wrap(status.KindInternal, message, err)
}

func buildEnv(extra map[string]string, metadata Metadata) []string {
	env := append([]string{}, os.Environ()...)
	keys := make([]string, 0, len(metadata))
	for key := range metadata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		envKey := "DEPLOYPIER_META_" + normalizeEnvKey(key)
		env = append(env, envKey+"="+metadata[key])
	}
	for key, value := range extra {
		env = append(env, key+"="+value)
	}
	return env
}

func normalizeEnvKey(value string) string {
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r - 32)
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		default:
			builder.WriteRune('_')
		}
	}
	return builder.String()
}

func AsExitError(err error, target **exec.ExitError) bool {
	if err == nil {
		return false
	}
	exitError, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	*target = exitError
	return true
}

func ExitCode(metadata Metadata, key string, fallback int) int {
	value, ok := metadata[key]
	if !ok {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
