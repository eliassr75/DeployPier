package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelfInstallerInstallsBinaryAndUpdatesProfileOnUnix(t *testing.T) {
	tempDir := t.TempDir()
	homeDir := cleanInstallPath("linux", filepath.Join(tempDir, "home"))
	sourceBinary := filepath.Join(tempDir, "deploypier-source")
	if err := os.WriteFile(sourceBinary, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write source binary: %v", err)
	}

	installer := &SelfInstaller{
		GOOS:           "linux",
		Shell:          "/bin/bash",
		HomeDir:        homeDir,
		PathValue:      "/usr/local/bin:/usr/bin",
		ExecutablePath: sourceBinary,
	}

	result, err := installer.Install("", true, false)
	if err != nil {
		t.Fatalf("install self: %v", err)
	}

	expectedBinary := joinInstallPath("linux", homeDir, ".local", "bin", "deploypier")
	if result.InstalledPath != expectedBinary {
		t.Fatalf("unexpected installed path: %s", result.InstalledPath)
	}
	if !result.PathUpdated {
		t.Fatalf("expected path update through shell profile")
	}
	if result.ProfilePath != joinInstallPath("linux", homeDir, ".profile") {
		t.Fatalf("unexpected profile path: %s", result.ProfilePath)
	}

	raw, err := os.ReadFile(filepath.FromSlash(expectedBinary))
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	if string(raw) != "binary" {
		t.Fatalf("unexpected installed binary content")
	}

	profileRaw, err := os.ReadFile(filepath.FromSlash(result.ProfilePath))
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	profileContent := string(profileRaw)
	if !strings.Contains(profileContent, `export PATH="$HOME/.local/bin":$PATH`) {
		t.Fatalf("expected PATH export inside profile, got: %s", profileContent)
	}
}

func TestSelfInstallerInstallsBinaryAndUpdatesUserPathOnWindows(t *testing.T) {
	tempDir := t.TempDir()
	sourceBinary := filepath.Join(tempDir, "deploypier-source.exe")
	if err := os.WriteFile(sourceBinary, []byte("binary"), 0o644); err != nil {
		t.Fatalf("write source binary: %v", err)
	}

	localAppData := filepath.Join(tempDir, "LocalAppData")
	var writtenUserPath string

	installer := &SelfInstaller{
		GOOS:           "windows",
		LocalAppData:   localAppData,
		PathValue:      `C:\Windows\System32`,
		ExecutablePath: sourceBinary,
		GetWindowsUserPath: func() (string, error) {
			return `C:\Users\demo\AppData\Local\Microsoft\WindowsApps`, nil
		},
		SetWindowsUserPath: func(value string) error {
			writtenUserPath = value
			return nil
		},
	}

	result, err := installer.Install("", true, false)
	if err != nil {
		t.Fatalf("install self: %v", err)
	}

	expectedDir := filepath.Join(localAppData, "Programs", "DeployPier")
	expectedBinary := filepath.Join(expectedDir, "deploypier.exe")
	if result.InstalledPath != expectedBinary {
		t.Fatalf("unexpected installed path: %s", result.InstalledPath)
	}
	if !result.PathUpdated {
		t.Fatalf("expected windows user PATH to be updated")
	}
	if !strings.Contains(writtenUserPath, expectedDir) {
		t.Fatalf("expected written PATH to contain install dir, got: %s", writtenUserPath)
	}
}

func TestSelfInstallerRespectsSkipPathAndForce(t *testing.T) {
	tempDir := t.TempDir()
	homeDir := cleanInstallPath("linux", filepath.Join(tempDir, "home"))
	sourceBinary := filepath.Join(tempDir, "deploypier-source")
	if err := os.WriteFile(sourceBinary, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write source binary: %v", err)
	}

	installer := &SelfInstaller{
		GOOS:           "linux",
		Shell:          "/bin/bash",
		HomeDir:        homeDir,
		PathValue:      "/usr/local/bin:/usr/bin",
		ExecutablePath: sourceBinary,
	}

	first, err := installer.Install("", false, false)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if first.PathUpdated {
		t.Fatalf("expected skip-path install to avoid profile changes")
	}

	if _, err := installer.Install("", false, false); err == nil {
		t.Fatalf("expected conflict when binary already exists without force")
	}

	second, err := installer.Install("", false, true)
	if err != nil {
		t.Fatalf("forced reinstall: %v", err)
	}
	if second.InstalledPath == "" {
		t.Fatalf("expected installed path on forced reinstall")
	}
}
