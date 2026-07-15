package config

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/deploypier/deploypier/internal/status"
	"gopkg.in/yaml.v3"
)

const EnvPrefix = "DEPLOYPIER_"
const LegacyEnvPrefix = "SHARED_HOST_DEPLOY_"

type Config struct {
	Project    ProjectConfig    `yaml:"project"`
	Build      BuildConfig      `yaml:"build"`
	Release    ReleaseConfig    `yaml:"release"`
	Transport  TransportConfig  `yaml:"transport"`
	Remote     RemoteConfig     `yaml:"remote"`
	Runtime    RuntimeConfig    `yaml:"runtime"`
	PostDeploy PostDeployConfig `yaml:"post_deploy"`
	State      StateConfig      `yaml:"state"`
	Activation ActivationConfig `yaml:"activation"`
	Hooks      HooksConfig      `yaml:"hooks"`
}

type ProjectConfig struct {
	Name      string `yaml:"name"`
	Root      string `yaml:"root"`
	Framework string `yaml:"framework"`
}

type BuildConfig struct {
	Include     []string `yaml:"include"`
	Exclude     []string `yaml:"exclude"`
	PHPCommand  string   `yaml:"php_command"`
	NodeCommand string   `yaml:"node_command"`
}

type ReleaseConfig struct {
	Directory string `yaml:"directory"`
	Retain    int    `yaml:"retain"`
}

type TransportConfig struct {
	Kind          string `yaml:"kind"`
	Protocol      string `yaml:"protocol"`
	Host          string `yaml:"host"`
	Port          int    `yaml:"port"`
	User          string `yaml:"user"`
	Path          string `yaml:"path"`
	KnownHosts    string `yaml:"known_hosts"`
	AllowInsecure bool   `yaml:"allow_insecure"`
	Password      string `yaml:"-"`
	PrivateKey    string `yaml:"-"`
}

type RemoteConfig struct {
	AppRoot    string `yaml:"app_root"`
	PublicRoot string `yaml:"public_root"`
	Layout     string `yaml:"layout"`
}

type RuntimeConfig struct {
	AppRoot        string `yaml:"app_root"`
	CurrentPointer string `yaml:"current_pointer"`
}

type PostDeployConfig struct {
	Mode       string `yaml:"mode"`
	HookURLEnv string `yaml:"hook_url_env"`
	KeyIDEnv   string `yaml:"key_id_env"`
	SecretEnv  string `yaml:"secret_env"`
	SmokeURL   string `yaml:"smoke_url"`
	HookURL    string `yaml:"-"`
	KeyID      string `yaml:"-"`
	Secret     string `yaml:"-"`
}

type StateConfig struct {
	File string `yaml:"file"`
}

type ActivationConfig struct {
	Kind           string `yaml:"kind"`
	CurrentPointer string `yaml:"current_pointer"`
}

type HookSpec struct {
	Name     string            `yaml:"name"`
	Command  []string          `yaml:"command"`
	Optional bool              `yaml:"optional"`
	Timeout  string            `yaml:"timeout"`
	Env      map[string]string `yaml:"env"`
}

type HooksConfig struct {
	BeforeBuild    []HookSpec `yaml:"before_build"`
	AfterBuild     []HookSpec `yaml:"after_build"`
	BeforePush     []HookSpec `yaml:"before_push"`
	AfterPush      []HookSpec `yaml:"after_push"`
	BeforeActivate []HookSpec `yaml:"before_activate"`
	AfterActivate  []HookSpec `yaml:"after_activate"`
	BeforeRollback []HookSpec `yaml:"before_rollback"`
	AfterRollback  []HookSpec `yaml:"after_rollback"`
}

func (h HooksConfig) ForPhase(phase string) []HookSpec {
	switch phase {
	case "before_build":
		return h.BeforeBuild
	case "after_build":
		return h.AfterBuild
	case "before_push":
		return h.BeforePush
	case "after_push":
		return h.AfterPush
	case "before_activate":
		return h.BeforeActivate
	case "after_activate":
		return h.AfterActivate
	case "before_rollback":
		return h.BeforeRollback
	case "after_rollback":
		return h.AfterRollback
	default:
		return nil
	}
}

func Load(path string, env map[string]string, cwd string) (Config, error) {
	configPath := path
	if configPath == "" {
		configPath = filepath.Join(cwd, "deploy.yml")
	} else if !filepath.IsAbs(configPath) {
		configPath = filepath.Join(cwd, configPath)
	}

	baseDir := filepath.Dir(configPath)
	cfg := defaults(cwd)

	raw, err := os.ReadFile(configPath)
	if err != nil {
		return Config{}, status.Wrap(status.KindConfig, "read config", err)
	}

	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		return Config{}, status.Wrap(status.KindConfig, "parse config", err)
	}

	resolveRelativePaths(&cfg, baseDir)
	applyEnvOverrides(&cfg, env, baseDir)
	normalizeRuntime(&cfg)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func defaults(cwd string) Config {
	baseStateDir := filepath.Join(cwd, ".deploypier")
	return Config{
		Project: ProjectConfig{
			Name:      filepath.Base(cwd),
			Root:      cwd,
			Framework: "laravel",
		},
		Build: BuildConfig{
			PHPCommand:  "composer install --no-dev --prefer-dist --optimize-autoloader",
			NodeCommand: "npm ci && npm run build",
		},
		Release: ReleaseConfig{
			Directory: filepath.Join(baseStateDir, "releases"),
			Retain:    5,
		},
		Transport: TransportConfig{
			Kind:     "local",
			Protocol: "local",
			Path:     filepath.Join(baseStateDir, "remote"),
			Port:     22,
		},
		Remote: RemoteConfig{
			AppRoot:    filepath.Join(baseStateDir, "remote", "app"),
			PublicRoot: filepath.Join(baseStateDir, "remote", "public_html"),
			Layout:     "release-based",
		},
		Runtime: RuntimeConfig{},
		PostDeploy: PostDeployConfig{
			Mode:       "skip",
			HookURLEnv: "DEPLOY_HOOK_URL",
			KeyIDEnv:   "DEPLOY_HOOK_KEY_ID",
			SecretEnv:  "DEPLOY_HOOK_SECRET",
		},
		State: StateConfig{
			File: filepath.Join(baseStateDir, "state.json"),
		},
		Activation: ActivationConfig{
			Kind:           "pointer",
			CurrentPointer: filepath.Join(baseStateDir, "remote", "current.txt"),
		},
	}
}

func resolveRelativePaths(cfg *Config, baseDir string) {
	cfg.Project.Root = resolvePath(baseDir, cfg.Project.Root)
	cfg.Release.Directory = resolvePath(baseDir, cfg.Release.Directory)
	cfg.Transport.Path = resolvePath(baseDir, cfg.Transport.Path)
	cfg.Remote.AppRoot = resolvePath(baseDir, cfg.Remote.AppRoot)
	cfg.Remote.PublicRoot = resolvePath(baseDir, cfg.Remote.PublicRoot)
	cfg.Runtime.AppRoot = resolvePath(baseDir, cfg.Runtime.AppRoot)
	cfg.Runtime.CurrentPointer = resolvePath(baseDir, cfg.Runtime.CurrentPointer)
	cfg.State.File = resolvePath(baseDir, cfg.State.File)
	cfg.Activation.CurrentPointer = resolvePath(baseDir, cfg.Activation.CurrentPointer)
}

func applyEnvOverrides(cfg *Config, env map[string]string, baseDir string) {
	if value := firstNonEmpty(env, EnvPrefix+"PROJECT_NAME", LegacyEnvPrefix+"PROJECT_NAME"); value != "" {
		cfg.Project.Name = value
	}
	if value := firstNonEmpty(env, EnvPrefix+"PROJECT_FRAMEWORK", LegacyEnvPrefix+"PROJECT_FRAMEWORK"); value != "" {
		cfg.Project.Framework = value
	}
	if value := firstNonEmpty(env, EnvPrefix+"PROJECT_ROOT", LegacyEnvPrefix+"PROJECT_ROOT"); value != "" {
		cfg.Project.Root = resolvePath(baseDir, value)
	}
	if value := firstNonEmpty(env, EnvPrefix+"BUILD_PHP_COMMAND", LegacyEnvPrefix+"BUILD_PHP_COMMAND"); value != "" {
		cfg.Build.PHPCommand = value
	}
	if value := firstNonEmpty(env, EnvPrefix+"BUILD_NODE_COMMAND", LegacyEnvPrefix+"BUILD_NODE_COMMAND"); value != "" {
		cfg.Build.NodeCommand = value
	}
	if value := firstNonEmpty(env, EnvPrefix+"BUILD_INCLUDE", LegacyEnvPrefix+"BUILD_INCLUDE"); value != "" {
		cfg.Build.Include = splitList(value)
	}
	if value := firstNonEmpty(env, EnvPrefix+"BUILD_EXCLUDE", LegacyEnvPrefix+"BUILD_EXCLUDE"); value != "" {
		cfg.Build.Exclude = splitList(value)
	}
	if value := firstNonEmpty(env, EnvPrefix+"RELEASE_DIRECTORY", LegacyEnvPrefix+"RELEASE_DIRECTORY"); value != "" {
		cfg.Release.Directory = resolvePath(baseDir, value)
	}
	if value := firstNonEmpty(env, EnvPrefix+"RELEASE_RETAIN", LegacyEnvPrefix+"RELEASE_RETAIN"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.Release.Retain = parsed
		}
	}
	if value := firstNonEmpty(env, EnvPrefix+"STATE_FILE", LegacyEnvPrefix+"STATE_FILE"); value != "" {
		cfg.State.File = resolvePath(baseDir, value)
	}
	if value := firstNonEmpty(env, EnvPrefix+"TRANSPORT_KIND", LegacyEnvPrefix+"TRANSPORT_KIND"); value != "" {
		cfg.Transport.Kind = value
	}
	if value := firstNonEmpty(env, EnvPrefix+"TRANSPORT_PROTOCOL", LegacyEnvPrefix+"TRANSPORT_PROTOCOL"); value != "" {
		cfg.Transport.Protocol = value
	}
	if value := firstNonEmpty(env, EnvPrefix+"TRANSPORT_HOST", LegacyEnvPrefix+"TRANSPORT_HOST", "DEPLOY_HOST"); value != "" {
		cfg.Transport.Host = value
	}
	if value := firstNonEmpty(env, EnvPrefix+"TRANSPORT_PORT", LegacyEnvPrefix+"TRANSPORT_PORT", "DEPLOY_PORT"); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.Transport.Port = parsed
		}
	}
	if value := firstNonEmpty(env, EnvPrefix+"TRANSPORT_USER", LegacyEnvPrefix+"TRANSPORT_USER", "DEPLOY_USER"); value != "" {
		cfg.Transport.User = value
	}
	if value := firstNonEmpty(env, EnvPrefix+"TRANSPORT_PATH", LegacyEnvPrefix+"TRANSPORT_PATH"); value != "" {
		cfg.Transport.Path = resolvePath(baseDir, value)
	}
	if value := firstNonEmpty(env, EnvPrefix+"TRANSPORT_KNOWN_HOSTS", LegacyEnvPrefix+"TRANSPORT_KNOWN_HOSTS"); value != "" {
		cfg.Transport.KnownHosts = resolvePath(baseDir, value)
	}
	if value := firstNonEmpty(env, EnvPrefix+"TRANSPORT_ALLOW_INSECURE", LegacyEnvPrefix+"TRANSPORT_ALLOW_INSECURE"); value != "" {
		cfg.Transport.AllowInsecure = strings.EqualFold(value, "true") || value == "1"
	}
	if value := firstNonEmpty(env, "DEPLOY_PASSWORD"); value != "" {
		cfg.Transport.Password = value
	}
	if value := firstNonEmpty(env, "DEPLOY_PRIVATE_KEY"); value != "" {
		cfg.Transport.PrivateKey = value
	}
	if value := firstNonEmpty(env, EnvPrefix+"REMOTE_APP_ROOT", LegacyEnvPrefix+"REMOTE_APP_ROOT", "DEPLOY_REMOTE_APP_ROOT"); value != "" {
		cfg.Remote.AppRoot = resolvePath(baseDir, value)
	}
	if value := firstNonEmpty(env, EnvPrefix+"REMOTE_PUBLIC_ROOT", LegacyEnvPrefix+"REMOTE_PUBLIC_ROOT", "DEPLOY_REMOTE_PUBLIC_ROOT"); value != "" {
		cfg.Remote.PublicRoot = resolvePath(baseDir, value)
	}
	if value := firstNonEmpty(env, EnvPrefix+"REMOTE_LAYOUT", LegacyEnvPrefix+"REMOTE_LAYOUT"); value != "" {
		cfg.Remote.Layout = value
	}
	if value := firstNonEmpty(env, EnvPrefix+"RUNTIME_APP_ROOT", LegacyEnvPrefix+"RUNTIME_APP_ROOT", "DEPLOY_RUNTIME_APP_ROOT"); value != "" {
		cfg.Runtime.AppRoot = resolvePath(baseDir, value)
	}
	if value := firstNonEmpty(env, EnvPrefix+"RUNTIME_CURRENT_POINTER", LegacyEnvPrefix+"RUNTIME_CURRENT_POINTER", "DEPLOY_RUNTIME_CURRENT_POINTER"); value != "" {
		cfg.Runtime.CurrentPointer = resolvePath(baseDir, value)
	}
	if value := firstNonEmpty(env, EnvPrefix+"POST_DEPLOY_MODE", LegacyEnvPrefix+"POST_DEPLOY_MODE"); value != "" {
		cfg.PostDeploy.Mode = value
	}
	if value := firstNonEmpty(env, EnvPrefix+"POST_DEPLOY_HOOK_URL_ENV", LegacyEnvPrefix+"POST_DEPLOY_HOOK_URL_ENV"); value != "" {
		cfg.PostDeploy.HookURLEnv = value
	}
	if value := firstNonEmpty(env, EnvPrefix+"POST_DEPLOY_KEY_ID_ENV", LegacyEnvPrefix+"POST_DEPLOY_KEY_ID_ENV"); value != "" {
		cfg.PostDeploy.KeyIDEnv = value
	}
	if value := firstNonEmpty(env, EnvPrefix+"POST_DEPLOY_SECRET_ENV", LegacyEnvPrefix+"POST_DEPLOY_SECRET_ENV"); value != "" {
		cfg.PostDeploy.SecretEnv = value
	}
	if value := firstNonEmpty(env, EnvPrefix+"POST_DEPLOY_SMOKE_URL", LegacyEnvPrefix+"POST_DEPLOY_SMOKE_URL"); value != "" {
		cfg.PostDeploy.SmokeURL = value
	}
	if cfg.PostDeploy.HookURLEnv != "" {
		cfg.PostDeploy.HookURL = env[cfg.PostDeploy.HookURLEnv]
	}
	if cfg.PostDeploy.KeyIDEnv != "" {
		cfg.PostDeploy.KeyID = env[cfg.PostDeploy.KeyIDEnv]
	}
	if cfg.PostDeploy.SecretEnv != "" {
		cfg.PostDeploy.Secret = env[cfg.PostDeploy.SecretEnv]
	}
	if value := firstNonEmpty(env, EnvPrefix+"ACTIVATION_KIND", LegacyEnvPrefix+"ACTIVATION_KIND"); value != "" {
		cfg.Activation.Kind = value
	}
	if value := firstNonEmpty(env, EnvPrefix+"ACTIVATION_POINTER", LegacyEnvPrefix+"ACTIVATION_POINTER"); value != "" {
		cfg.Activation.CurrentPointer = resolvePath(baseDir, value)
	}
}

func normalizeRuntime(cfg *Config) {
	if strings.TrimSpace(cfg.Runtime.AppRoot) == "" {
		cfg.Runtime.AppRoot = cfg.Remote.AppRoot
	}
	if strings.TrimSpace(cfg.Runtime.CurrentPointer) == "" {
		cfg.Runtime.CurrentPointer = cfg.Activation.CurrentPointer
	}
}

func firstNonEmpty(env map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(env[key]); value != "" {
			return value
		}
	}
	return ""
}

func splitList(value string) []string {
	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func resolvePath(baseDir, value string) string {
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "/") {
		return path.Clean(filepath.ToSlash(value))
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(baseDir, value))
}

func (c Config) Validate() error {
	var problems []string

	if c.Project.Name == "" {
		problems = append(problems, "project.name is required")
	}
	if c.Project.Framework == "" {
		problems = append(problems, "project.framework is required")
	}
	if c.Project.Root == "" {
		problems = append(problems, "project.root is required")
	}
	if c.Build.PHPCommand == "" {
		problems = append(problems, "build.php_command is required")
	}
	if c.Build.NodeCommand == "" {
		problems = append(problems, "build.node_command is required")
	}
	if c.Release.Directory == "" {
		problems = append(problems, "release.directory is required")
	}
	if c.Release.Retain < 1 {
		problems = append(problems, "release.retain must be at least 1")
	}
	if c.Transport.Kind == "" {
		problems = append(problems, "transport.kind is required")
	}
	if c.Transport.Protocol == "" {
		problems = append(problems, "transport.protocol is required")
	}
	if c.Transport.Protocol != "" && !contains([]string{"local", "ftp", "ftps", "sftp", "ssh"}, c.Transport.Protocol) {
		problems = append(problems, "transport.protocol must be one of local, ftp, ftps, sftp, ssh")
	}
	if c.Transport.Path == "" {
		problems = append(problems, "transport.path is required")
	}
	if c.Transport.Protocol == "local" && c.Transport.Path == "" {
		problems = append(problems, "transport.path is required for local transport")
	}
	if c.Transport.Protocol != "local" {
		if c.Transport.Host == "" {
			problems = append(problems, "transport.host is required for remote transport")
		}
		if c.Transport.User == "" {
			problems = append(problems, "transport.user is required for remote transport")
		}
	}
	if c.Transport.Protocol == "ftp" && !c.Transport.AllowInsecure {
		problems = append(problems, "transport.protocol=ftp requires transport.allow_insecure=true")
	}
	if c.Transport.Protocol == "ftps" && c.Transport.Password == "" {
		problems = append(problems, "DEPLOY_PASSWORD is required for ftps transport")
	}
	if (c.Transport.Protocol == "sftp" || c.Transport.Protocol == "ssh") && c.Transport.Password == "" && c.Transport.PrivateKey == "" {
		problems = append(problems, "DEPLOY_PASSWORD or DEPLOY_PRIVATE_KEY is required for sftp transport")
	}
	if c.Remote.AppRoot == "" {
		problems = append(problems, "remote.app_root is required")
	}
	if c.Remote.PublicRoot == "" {
		problems = append(problems, "remote.public_root is required")
	}
	if c.Remote.Layout == "" {
		problems = append(problems, "remote.layout is required")
	}
	if c.Remote.Layout != "" && !contains([]string{"auto", "release-based", "in-place"}, c.Remote.Layout) {
		problems = append(problems, "remote.layout must be one of auto, release-based, in-place")
	}
	if c.PostDeploy.Mode == "" {
		problems = append(problems, "post_deploy.mode is required")
	}
	if c.PostDeploy.Mode != "" && !contains([]string{"auto", "manual", "skip", "bypass"}, c.PostDeploy.Mode) {
		problems = append(problems, "post_deploy.mode must be one of auto, manual, skip, bypass")
	}
	if c.State.File == "" {
		problems = append(problems, "state.file is required")
	}
	if c.Runtime.AppRoot == "" {
		problems = append(problems, "runtime.app_root is required")
	}
	if c.Activation.Kind == "" {
		problems = append(problems, "activation.kind is required")
	}
	if c.Activation.CurrentPointer == "" && c.Activation.Kind == "pointer" {
		problems = append(problems, "activation.current_pointer is required for pointer activation")
	}
	if c.Runtime.CurrentPointer == "" && c.Activation.Kind == "pointer" {
		problems = append(problems, "runtime.current_pointer is required for pointer activation")
	}

	for phase, hooks := range map[string][]HookSpec{
		"before_build":    c.Hooks.BeforeBuild,
		"after_build":     c.Hooks.AfterBuild,
		"before_push":     c.Hooks.BeforePush,
		"after_push":      c.Hooks.AfterPush,
		"before_activate": c.Hooks.BeforeActivate,
		"after_activate":  c.Hooks.AfterActivate,
		"before_rollback": c.Hooks.BeforeRollback,
		"after_rollback":  c.Hooks.AfterRollback,
	} {
		for index, hook := range hooks {
			if len(hook.Command) == 0 {
				problems = append(problems, fmt.Sprintf("hooks.%s[%d].command is required", phase, index))
			}
		}
	}

	if len(problems) > 0 {
		return status.Wrap(status.KindConfig, "validate config", errors.New(strings.Join(problems, "; ")))
	}
	return nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
