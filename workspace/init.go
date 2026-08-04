package workspace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DefaultProfileName is used when the caller does not name a profile.
const DefaultProfileName = "default"

// InitOptions are the inputs of Init.
type InitOptions struct {
	Ref               // ConfigDir (empty: OS default) and Profile (empty: "default")
	Workspace  string // working directory of the profile; empty: none
	Force      bool   // overwrite an existing profile
	CLIVersion string // recorded in the metadata of a fresh config
}

// InitResult describes what was created, and is what the CLI prints.
type InitResult struct {
	ConfigDir        string `json:"config_dir"`
	ConfigFile       string `json:"config_file"`
	CredentialsFile  string `json:"credentials_file"`
	ProfilesDir      string `json:"profiles_dir"`
	Profile          string `json:"profile"`
	DefaultProfile   string `json:"default_profile"`
	Workspace        string `json:"workspace,omitempty"`
	CreatedWorkspace bool   `json:"created_workspace"`
	AlreadyExisted   bool   `json:"already_existed"`
}

// Init creates (or extends) the workspace: config directory, config.json, the
// profiles directory and optionally the working directory of the profile — all
// of it owner-only. The credentials file is not touched here; it belongs to the
// auth package, and the init tool asks that package to place it.
func Init(opts InitOptions) (*InitResult, error) {
	dir := ResolveDir(opts.ConfigDir)
	profileName := opts.Profile
	if profileName == "" {
		profileName = DefaultProfileName
	}
	if opts.CLIVersion == "" {
		opts.CLIVersion = "dev"
	}

	if err := os.MkdirAll(dir, DirPerm); err != nil {
		return nil, fmt.Errorf("create config directory %s: %w", dir, err)
	}
	if err := os.MkdirAll(ProfilesDir(dir), DirPerm); err != nil {
		return nil, fmt.Errorf("create profiles directory: %w", err)
	}

	cfg, err := Load(dir)
	existed := true
	if err != nil {
		if !errors.Is(err, ErrNotInitialized) {
			return nil, err
		}
		cfg = New(opts.CLIVersion)
		existed = false
	}

	wsDir := opts.Workspace
	if wsDir != "" {
		abs, err := filepath.Abs(wsDir)
		if err != nil {
			return nil, fmt.Errorf("resolve workspace %s: %w", wsDir, err)
		}
		wsDir = abs
	}

	profile := &Profile{
		Name:      profileName,
		Workspace: wsDir,
		CreatedAt: time.Now().UTC(),
	}
	// Keep the sources of an existing profile when overwriting with -force.
	if old, ok := cfg.Profiles[profileName]; ok && opts.Force {
		profile.Sources = old.Sources
		if profile.Workspace == "" {
			profile.Workspace = old.Workspace
		}
	}
	if err := cfg.AddProfile(profile, opts.Force); err != nil {
		return nil, err
	}
	if err := cfg.Save(dir); err != nil {
		return nil, err
	}

	createdWorkspace := false
	if profile.Workspace != "" {
		if _, statErr := os.Stat(profile.Workspace); errors.Is(statErr, os.ErrNotExist) {
			if err := os.MkdirAll(profile.Workspace, DirPerm); err != nil {
				return nil, fmt.Errorf("create workspace %s: %w", profile.Workspace, err)
			}
			createdWorkspace = true
		}
	}

	return &InitResult{
		ConfigDir:        dir,
		ConfigFile:       FilePath(dir),
		CredentialsFile:  CredentialsPath(dir),
		ProfilesDir:      ProfilesDir(dir),
		Profile:          profile.Name,
		DefaultProfile:   cfg.DefaultProfile,
		Workspace:        profile.Workspace,
		CreatedWorkspace: createdWorkspace,
		AlreadyExisted:   existed,
	}, nil
}
