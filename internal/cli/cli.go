package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/deploypier/deploypier/internal/app"
	"github.com/deploypier/deploypier/internal/config"
	"github.com/deploypier/deploypier/internal/install"
	"github.com/deploypier/deploypier/internal/version"
)

func Run(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer, env []string, cwd string) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}

	switch args[0] {
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	case "version":
		_, err := fmt.Fprintf(stdout, "%s\n", version.String)
		return err
	case "doctor":
		return runDoctor(ctx, args[1:], stdout, env, cwd)
	case "plan":
		return runPlan(ctx, args[1:], stdout, env, cwd)
	case "build":
		return runBuild(ctx, args[1:], stdout, env, cwd)
	case "push":
		return runPush(ctx, args[1:], stdout, env, cwd)
	case "rollback":
		return runRollback(ctx, args[1:], stdout, env, cwd)
	case "install-self":
		return runInstallSelf(args[1:], stdout)
	case "install-laravel-hook":
		return runInstallLaravelHook(args[1:], stdout, cwd)
	case "install-locaweb-bootstrap":
		return runInstallLocawebBootstrap(args[1:], stdout, cwd)
	case "init-locaweb":
		return runInitLocaweb(args[1:], stdout, cwd)
	default:
		printUsage(stderr)
		return errors.New("unknown command: " + args[0])
	}
}

func runDoctor(ctx context.Context, args []string, stdout io.Writer, env []string, cwd string) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to deploy.yml")
	envFile := flags.String("env-file", "", "path to .deploy.env file")
	if err := flags.Parse(args); err != nil {
		return err
	}

	service, err := loadService(*configPath, *envFile, env, cwd)
	if err != nil {
		return err
	}

	checks, err := service.Doctor(ctx)
	if err != nil {
		return err
	}
	for _, check := range checks {
		if _, err := fmt.Fprintf(stdout, "[%s] %s: %s", strings.ToUpper(string(check.Report.Level)), check.Name, check.Report.Message); err != nil {
			return err
		}
		if check.Details != "" {
			if _, err := fmt.Fprintf(stdout, " (%s)", check.Details); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(stdout); err != nil {
			return err
		}
	}
	return nil
}

func runPlan(ctx context.Context, args []string, stdout io.Writer, env []string, cwd string) error {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to deploy.yml")
	envFile := flags.String("env-file", "", "path to .deploy.env file")
	if err := flags.Parse(args); err != nil {
		return err
	}

	service, err := loadService(*configPath, *envFile, env, cwd)
	if err != nil {
		return err
	}

	plan, err := service.Plan(ctx)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(stdout, "project: %s\nsource_root: %s\nrelease_dir: %s\ntransport: %s\nactivation: %s\nlatest_build: %s\ncurrent_release: %s\n",
		plan.Project, plan.SourceRoot, plan.ReleaseDir, plan.Transport, plan.Activation, emptyDash(plan.LatestBuild), emptyDash(plan.CurrentRelease)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(stdout, "layout: %s\npost_deploy: %s\n", plan.Layout, plan.PostDeployMode); err != nil {
		return err
	}
	for _, step := range plan.Steps {
		if _, err := fmt.Fprintf(stdout, "- %s\n", step); err != nil {
			return err
		}
	}
	return nil
}

func runBuild(ctx context.Context, args []string, stdout io.Writer, env []string, cwd string) error {
	flags := flag.NewFlagSet("build", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to deploy.yml")
	envFile := flags.String("env-file", "", "path to .deploy.env file")
	if err := flags.Parse(args); err != nil {
		return err
	}

	service, err := loadService(*configPath, *envFile, env, cwd)
	if err != nil {
		return err
	}

	release, err := service.Build(ctx)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(stdout, "release_id: %s\nrelease_path: %s\nmanifest: %s\n", release.ID, release.Path, release.ManifestPath)
	return err
}

func runPush(ctx context.Context, args []string, stdout io.Writer, env []string, cwd string) error {
	flags := flag.NewFlagSet("push", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to deploy.yml")
	envFile := flags.String("env-file", "", "path to .deploy.env file")
	releaseID := flags.String("release", "", "release id to push")
	skipActivate := flags.Bool("skip-activate", false, "skip activation after upload")
	if err := flags.Parse(args); err != nil {
		return err
	}

	service, err := loadService(*configPath, *envFile, env, cwd)
	if err != nil {
		return err
	}

	result, err := service.Push(ctx, *releaseID, *skipActivate)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(stdout, "status: %s\nrelease_id: %s\nremote_path: %s\nactivated: %t\nactivated_path: %s\n", result.Status, result.ReleaseID, result.RemotePath, result.Activated, emptyDash(result.ActivatedPath))
	if err != nil {
		return err
	}
	for _, warning := range result.Warnings {
		if _, err := fmt.Fprintf(stdout, "warning: %s\n", warning); err != nil {
			return err
		}
	}
	return err
}

func runRollback(ctx context.Context, args []string, stdout io.Writer, env []string, cwd string) error {
	flags := flag.NewFlagSet("rollback", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "path to deploy.yml")
	envFile := flags.String("env-file", "", "path to .deploy.env file")
	releaseID := flags.String("release", "", "release id to activate")
	if err := flags.Parse(args); err != nil {
		return err
	}

	service, err := loadService(*configPath, *envFile, env, cwd)
	if err != nil {
		return err
	}

	activatedPath, err := service.Rollback(ctx, *releaseID)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(stdout, "activated_path: %s\n", activatedPath)
	return err
}

func runInstallSelf(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("install-self", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	targetDir := flags.String("target-dir", "", "directory where the deploypier binary should be installed")
	skipPath := flags.Bool("skip-path", false, "do not try to register the install directory in PATH")
	force := flags.Bool("force", false, "overwrite the installed binary when it already exists")
	if err := flags.Parse(args); err != nil {
		return err
	}

	installer := install.NewSelfInstaller()
	result, err := installer.Install(*targetDir, !*skipPath, *force)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(stdout, "installed_path: %s\ntarget_dir: %s\n", result.InstalledPath, result.TargetDir); err != nil {
		return err
	}
	switch {
	case result.PathUpdated:
		if _, err := fmt.Fprintf(stdout, "path_status: updated\n"); err != nil {
			return err
		}
	case result.AlreadyInPath:
		if _, err := fmt.Fprintf(stdout, "path_status: already_available\n"); err != nil {
			return err
		}
	default:
		if _, err := fmt.Fprintf(stdout, "path_status: skipped\n"); err != nil {
			return err
		}
	}
	if result.PathMode != "" {
		if _, err := fmt.Fprintf(stdout, "path_mode: %s\n", result.PathMode); err != nil {
			return err
		}
	}
	if result.ProfilePath != "" {
		if _, err := fmt.Fprintf(stdout, "profile_path: %s\n", result.ProfilePath); err != nil {
			return err
		}
	}
	if result.RestartRequired {
		if _, err := fmt.Fprintln(stdout, "note: reopen the terminal session so the updated PATH becomes available."); err != nil {
			return err
		}
	}
	return nil
}

func runInstallLaravelHook(args []string, stdout io.Writer, cwd string) error {
	flags := flag.NewFlagSet("install-laravel-hook", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	projectRoot := flags.String("project-root", cwd, "Laravel project root")
	force := flags.Bool("force", false, "overwrite generated files when they already exist")
	if err := flags.Parse(args); err != nil {
		return err
	}

	root := *projectRoot
	if !filepath.IsAbs(root) {
		root = filepath.Join(cwd, root)
	}

	installer := install.NewLaravelHookInstaller()
	created, err := installer.Install(root, *force)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(stdout, "project_root: %s\n", root); err != nil {
		return err
	}
	for _, path := range created {
		if _, err := fmt.Fprintf(stdout, "created: %s\n", path); err != nil {
			return err
		}
	}
	return nil
}

func runInstallLocawebBootstrap(args []string, stdout io.Writer, cwd string) error {
	flags := flag.NewFlagSet("install-locaweb-bootstrap", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	projectRoot := flags.String("project-root", cwd, "Laravel project root")
	ftpUser := flags.String("ftp-user", "", "Locaweb FTP user")
	force := flags.Bool("force", false, "overwrite generated files when they already exist")
	if err := flags.Parse(args); err != nil {
		return err
	}

	root := *projectRoot
	if !filepath.IsAbs(root) {
		root = filepath.Join(cwd, root)
	}

	installer := install.NewLocawebBootstrapInstaller()
	created, err := installer.Install(root, *ftpUser, *force)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(stdout, "project_root: %s\n", root); err != nil {
		return err
	}
	for _, path := range created {
		if _, err := fmt.Fprintf(stdout, "created: %s\n", path); err != nil {
			return err
		}
	}
	return nil
}

func runInitLocaweb(args []string, stdout io.Writer, cwd string) error {
	flags := flag.NewFlagSet("init-locaweb", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	projectRoot := flags.String("project-root", cwd, "Laravel project root")
	ftpUser := flags.String("ftp-user", "", "Locaweb FTP user")
	force := flags.Bool("force", false, "overwrite generated files when they already exist")
	if err := flags.Parse(args); err != nil {
		return err
	}

	root := *projectRoot
	if !filepath.IsAbs(root) {
		root = filepath.Join(cwd, root)
	}

	initializer := install.NewLocawebConfigInitializer()
	created, err := initializer.Install(root, *ftpUser, *force)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(stdout, "project_root: %s\n", root); err != nil {
		return err
	}
	for _, path := range created {
		if _, err := fmt.Fprintf(stdout, "created: %s\n", path); err != nil {
			return err
		}
	}
	return nil
}

func loadService(configPath string, envFile string, env []string, cwd string) (*app.Service, error) {
	resolvedEnv, err := resolveEnv(configPath, envFile, env, cwd)
	if err != nil {
		return nil, err
	}

	cfg, err := config.Load(configPath, resolvedEnv, cwd)
	if err != nil {
		return nil, err
	}
	return app.New(cfg)
}

func envMap(values []string) map[string]string {
	result := make(map[string]string, len(values))
	for _, entry := range values {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}

func resolveEnv(configPath string, envFile string, env []string, cwd string) (map[string]string, error) {
	resolved := envMap(env)

	path := envFile
	if strings.TrimSpace(path) == "" {
		baseDir := cwd
		if strings.TrimSpace(configPath) != "" {
			pathCandidate := configPath
			if !filepath.IsAbs(pathCandidate) {
				pathCandidate = filepath.Join(cwd, pathCandidate)
			}
			baseDir = filepath.Dir(pathCandidate)
		}
		path = filepath.Join(baseDir, ".deploy.env")
	} else if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}

	fileValues, err := parseEnvFile(path)
	if err != nil {
		return nil, err
	}
	for key, value := range fileValues {
		if _, exists := resolved[key]; !exists {
			resolved[key] = value
		}
	}
	return resolved, nil
}

func parseEnvFile(path string) (map[string]string, error) {
	if strings.TrimSpace(path) == "" {
		return map[string]string{}, nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if key != "" {
			values[key] = value
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func emptyDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "deploypier")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  doctor    Validate config, local state, and transport readiness")
	fmt.Fprintln(w, "  plan      Print the local execution plan")
	fmt.Fprintln(w, "  build     Create a local release bundle and manifest")
	fmt.Fprintln(w, "  push      Upload a release and optionally activate it")
	fmt.Fprintln(w, "  rollback  Activate the previous or requested release")
	fmt.Fprintln(w, "  install-self  Install the current DeployPier binary in a user PATH directory")
	fmt.Fprintln(w, "  init-locaweb  Generate deploy.yml and environment examples for Locaweb")
	fmt.Fprintln(w, "  install-laravel-hook  Generate the Laravel receiver files in a target project")
	fmt.Fprintln(w, "  install-locaweb-bootstrap  Generate first-time Locaweb bootstrap scripts")
	fmt.Fprintln(w, "  version   Print the CLI version")
}
