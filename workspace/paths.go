package workspace

import (
	"os"
	"path/filepath"
	"runtime"
)

// AppDirName is the directory the tools own inside the OS config location.
const AppDirName = "dataprep"

// File names inside the config directory.
const (
	ConfigFileName      = "config.json"
	CredentialsFileName = "credentials.json"
	ProfilesDirName     = "profiles"
)

// Permissions: config material is owner only, on every platform that supports it.
const (
	DirPerm  os.FileMode = 0o700
	FilePerm os.FileMode = 0o600
)

// EnvConfigDir overrides the config directory (used by the container images).
const EnvConfigDir = "DATAPREP_CONFIG_DIR"

// DefaultDir resolves the config directory following OS convention:
//
//	$DATAPREP_CONFIG_DIR                 (explicit override, all platforms)
//	%APPDATA%\dataprep                   (Windows)
//	$XDG_CONFIG_HOME/dataprep            (Linux/macOS, when set)
//	~/.config/dataprep                   (Linux/macOS fallback)
func DefaultDir() string {
	if v := os.Getenv(EnvConfigDir); v != "" {
		return v
	}
	if runtime.GOOS == "windows" {
		if appData := os.Getenv("APPDATA"); appData != "" {
			return filepath.Join(appData, AppDirName)
		}
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, AppDirName)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", AppDirName)
	}
	return "." + AppDirName
}

// ResolveDir returns the directory to use, preferring an explicit value.
func ResolveDir(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return DefaultDir()
}

func FilePath(dir string) string        { return filepath.Join(dir, ConfigFileName) }
func CredentialsPath(dir string) string { return filepath.Join(dir, CredentialsFileName) }
func ProfilesDir(dir string) string     { return filepath.Join(dir, ProfilesDirName) }
