package install

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/deploypier/deploypier/internal/status"
)

type SelfInstaller struct {
	GOOS               string
	Shell              string
	HomeDir            string
	LocalAppData       string
	PathValue          string
	ExecutablePath     string
	GetWindowsUserPath func() (string, error)
	SetWindowsUserPath func(string) error
}

type SelfInstallResult struct {
	InstalledPath   string
	TargetDir       string
	PathUpdated     bool
	AlreadyInPath   bool
	PathMode        string
	ProfilePath     string
	RestartRequired bool
}

func NewSelfInstaller() *SelfInstaller {
	homeDir, _ := os.UserHomeDir()
	executablePath, _ := os.Executable()

	return &SelfInstaller{
		GOOS:               runtime.GOOS,
		Shell:              os.Getenv("SHELL"),
		HomeDir:            homeDir,
		LocalAppData:       os.Getenv("LOCALAPPDATA"),
		PathValue:          os.Getenv("PATH"),
		ExecutablePath:     executablePath,
		GetWindowsUserPath: readWindowsUserPath,
		SetWindowsUserPath: writeWindowsUserPath,
	}
}

func (i *SelfInstaller) Install(targetDir string, updatePath bool, force bool) (SelfInstallResult, error) {
	if strings.TrimSpace(i.ExecutablePath) == "" {
		return SelfInstallResult{}, status.Wrap(status.KindConfig, "install self", fmt.Errorf("current executable path is unavailable"))
	}

	resolvedTargetDir := strings.TrimSpace(targetDir)
	if resolvedTargetDir == "" {
		var err error
		resolvedTargetDir, err = i.defaultTargetDir()
		if err != nil {
			return SelfInstallResult{}, err
		}
	}

	resolvedTargetDir = cleanInstallPath(i.GOOS, resolvedTargetDir)
	binaryPath := joinInstallPath(i.GOOS, resolvedTargetDir, binaryName(i.GOOS))

	if _, err := os.Stat(binaryPath); err == nil && !force {
		return SelfInstallResult{}, status.Wrap(status.KindConflict, "install self", fmt.Errorf("target binary already exists: %s", binaryPath))
	} else if err != nil && !os.IsNotExist(err) {
		return SelfInstallResult{}, status.Wrap(status.KindInternal, "install self", err)
	}

	if err := os.MkdirAll(resolvedTargetDir, 0o755); err != nil {
		return SelfInstallResult{}, status.Wrap(status.KindInternal, "install self", err)
	}
	if err := copyFile(i.ExecutablePath, binaryPath, i.GOOS != "windows"); err != nil {
		return SelfInstallResult{}, err
	}

	result := SelfInstallResult{
		InstalledPath: binaryPath,
		TargetDir:     resolvedTargetDir,
	}

	if !updatePath {
		return result, nil
	}

	if pathContainsDir(i.GOOS, i.PathValue, resolvedTargetDir) {
		result.AlreadyInPath = true
		return result, nil
	}

	if i.GOOS == "windows" {
		currentUserPath, err := i.readWindowsUserPath()
		if err != nil {
			return result, err
		}
		if pathContainsDir(i.GOOS, currentUserPath, resolvedTargetDir) {
			result.AlreadyInPath = true
			result.RestartRequired = true
			result.PathMode = "user-environment"
			return result, nil
		}

		newUserPath := appendPathEntry(i.GOOS, currentUserPath, resolvedTargetDir)
		if err := i.writeWindowsUserPath(newUserPath); err != nil {
			return result, err
		}

		result.PathUpdated = true
		result.PathMode = "user-environment"
		result.RestartRequired = true
		return result, nil
	}

	profilePath, snippet, markers, err := i.profileUpdatePlan(resolvedTargetDir)
	if err != nil {
		return result, err
	}

	changed, err := ensureProfileContains(profilePath, snippet, markers)
	if err != nil {
		return result, err
	}

	result.ProfilePath = profilePath
	result.PathMode = "shell-profile"
	result.PathUpdated = changed
	result.RestartRequired = changed
	if !changed {
		result.AlreadyInPath = true
	}

	return result, nil
}

func (i *SelfInstaller) defaultTargetDir() (string, error) {
	switch i.GOOS {
	case "windows":
		if strings.TrimSpace(i.LocalAppData) == "" {
			return "", status.Wrap(status.KindConfig, "install self", fmt.Errorf("LOCALAPPDATA is not available"))
		}
		return joinInstallPath(i.GOOS, i.LocalAppData, "Programs", "DeployPier"), nil
	default:
		if strings.TrimSpace(i.HomeDir) == "" {
			return "", status.Wrap(status.KindConfig, "install self", fmt.Errorf("home directory is not available"))
		}
		return joinInstallPath(i.GOOS, i.HomeDir, ".local", "bin"), nil
	}
}

func (i *SelfInstaller) profileUpdatePlan(targetDir string) (string, string, []string, error) {
	if strings.TrimSpace(i.HomeDir) == "" {
		return "", "", nil, status.Wrap(status.KindConfig, "install self", fmt.Errorf("home directory is not available"))
	}

	shellName := strings.TrimSpace(filepath.Base(i.Shell))
	defaultTarget := joinInstallPath(i.GOOS, i.HomeDir, ".local", "bin")
	isDefaultTarget := samePath(i.GOOS, targetDir, defaultTarget)

	switch shellName {
	case "fish":
		profile := joinInstallPath(i.GOOS, i.HomeDir, ".config", "fish", "config.fish")
		entry := targetDir
		snippet := fmt.Sprintf("\n# Added by DeployPier\nfish_add_path -m %s\n", quoteFishPath(entry, isDefaultTarget))
		markers := []string{entry}
		if isDefaultTarget {
			markers = append(markers, "~/.local/bin")
		}
		return profile, snippet, markers, nil
	case "zsh":
		profile := joinInstallPath(i.GOOS, i.HomeDir, ".zprofile")
		entry := targetDir
		snippet := fmt.Sprintf("\n# Added by DeployPier\nexport PATH=%q:$PATH\n", shellPathToken(entry, i.HomeDir, isDefaultTarget))
		markers := []string{entry}
		if isDefaultTarget {
			markers = append(markers, "$HOME/.local/bin")
		}
		return profile, snippet, markers, nil
	default:
		profile := joinInstallPath(i.GOOS, i.HomeDir, ".profile")
		entry := targetDir
		snippet := fmt.Sprintf("\n# Added by DeployPier\nexport PATH=%q:$PATH\n", shellPathToken(entry, i.HomeDir, isDefaultTarget))
		markers := []string{entry}
		if isDefaultTarget {
			markers = append(markers, "$HOME/.local/bin")
		}
		return profile, snippet, markers, nil
	}
}

func (i *SelfInstaller) readWindowsUserPath() (string, error) {
	if i.GetWindowsUserPath == nil {
		return "", status.Wrap(status.KindConfig, "install self", fmt.Errorf("windows path reader is unavailable"))
	}
	value, err := i.GetWindowsUserPath()
	if err != nil {
		return "", status.Wrap(status.KindInternal, "read windows user path", err)
	}
	return strings.TrimSpace(value), nil
}

func (i *SelfInstaller) writeWindowsUserPath(value string) error {
	if i.SetWindowsUserPath == nil {
		return status.Wrap(status.KindConfig, "install self", fmt.Errorf("windows path writer is unavailable"))
	}
	if err := i.SetWindowsUserPath(value); err != nil {
		return status.Wrap(status.KindInternal, "write windows user path", err)
	}
	return nil
}

func ensureProfileContains(profilePath string, snippet string, markers []string) (bool, error) {
	raw, err := os.ReadFile(profilePath)
	if err != nil && !os.IsNotExist(err) {
		return false, status.Wrap(status.KindInternal, "read shell profile", err)
	}

	content := string(raw)
	for _, marker := range markers {
		if strings.TrimSpace(marker) != "" && strings.Contains(content, marker) {
			return false, nil
		}
	}
	if strings.Contains(content, strings.TrimSpace(snippet)) {
		return false, nil
	}

	if err := os.MkdirAll(filepath.Dir(profilePath), 0o755); err != nil {
		return false, status.Wrap(status.KindInternal, "mkdir shell profile", err)
	}

	var builder strings.Builder
	builder.WriteString(content)
	builder.WriteString(snippet)
	if err := os.WriteFile(profilePath, []byte(builder.String()), 0o644); err != nil {
		return false, status.Wrap(status.KindInternal, "write shell profile", err)
	}
	return true, nil
}

func shellPathToken(targetDir string, homeDir string, isDefaultTarget bool) string {
	if isDefaultTarget {
		return "$HOME/.local/bin"
	}
	return targetDir
}

func quoteFishPath(targetDir string, isDefaultTarget bool) string {
	if isDefaultTarget {
		return "~/.local/bin"
	}
	return fmt.Sprintf("%q", targetDir)
}

func binaryName(goos string) string {
	if goos == "windows" {
		return "deploypier.exe"
	}
	return "deploypier"
}

func cleanInstallPath(goos string, value string) string {
	if goos == "windows" {
		return filepath.Clean(value)
	}
	return path.Clean(strings.ReplaceAll(value, `\`, `/`))
}

func joinInstallPath(goos string, first string, more ...string) string {
	if goos == "windows" {
		parts := append([]string{first}, more...)
		return filepath.Clean(filepath.Join(parts...))
	}
	parts := append([]string{first}, more...)
	return path.Clean(path.Join(parts...))
}

func samePath(goos string, left string, right string) bool {
	return normalizePath(goos, left) == normalizePath(goos, right)
}

func pathContainsDir(goos string, pathValue string, targetDir string) bool {
	separator := ":"
	if goos == "windows" {
		separator = ";"
	}
	target := normalizePath(goos, targetDir)
	for _, entry := range strings.Split(pathValue, separator) {
		if normalizePath(goos, entry) == target {
			return true
		}
	}
	return false
}

func appendPathEntry(goos string, pathValue string, targetDir string) string {
	separator := ":"
	if goos == "windows" {
		separator = ";"
	}
	if strings.TrimSpace(pathValue) == "" {
		return targetDir
	}
	if pathContainsDir(goos, pathValue, targetDir) {
		return pathValue
	}
	return strings.TrimRight(pathValue, separator) + separator + targetDir
}

func normalizePath(goos string, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if goos == "windows" {
		return strings.ToLower(filepath.Clean(strings.ReplaceAll(trimmed, "/", `\`)))
	}
	return path.Clean(strings.ReplaceAll(trimmed, `\`, `/`))
}

func copyFile(sourcePath string, targetPath string, executable bool) error {
	in, err := os.Open(sourcePath)
	if err != nil {
		return status.Wrap(status.KindInternal, "read current executable", err)
	}
	defer in.Close()

	out, err := os.Create(targetPath)
	if err != nil {
		return status.Wrap(status.KindInternal, "create installed executable", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return status.Wrap(status.KindInternal, "copy installed executable", err)
	}
	if err := out.Close(); err != nil {
		return status.Wrap(status.KindInternal, "close installed executable", err)
	}
	if executable {
		if err := os.Chmod(targetPath, 0o755); err != nil {
			return status.Wrap(status.KindInternal, "chmod installed executable", err)
		}
	}
	return nil
}

func readWindowsUserPath() (string, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", "[Environment]::GetEnvironmentVariable('Path', 'User')")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func writeWindowsUserPath(value string) error {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", "[Environment]::SetEnvironmentVariable('Path', $env:DEPLOYPIER_NEW_PATH, 'User')")
	cmd.Env = append(os.Environ(), "DEPLOYPIER_NEW_PATH="+value)
	return cmd.Run()
}
