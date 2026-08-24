// Inspecting and editing the configuration. Anything returned for printing is
// already redacted.
package workspace

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// ShowResult is the effective configuration, secrets masked.
type ShowResult struct {
	ConfigDir string  `json:"config_dir"`
	Profile   string  `json:"profile"`
	Config    *Config `json:"config"`
}

// PathsResult lists where everything lives, for scripts and docs.
type PathsResult struct {
	ConfigDir       string `json:"config_dir"`
	ConfigFile      string `json:"config_file"`
	CredentialsFile string `json:"credentials_file"`
	ProfilesDir     string `json:"profiles_dir"`
	Source          string `json:"source"` // how the directory was chosen
}

// SourceResult is returned after a source was added or removed.
type SourceResult struct {
	Profile string `json:"profile"`
	Source  Source `json:"source"`
	Removed bool   `json:"removed,omitempty"`
}

// ProfileResult is returned after the default profile changed.
type ProfileResult struct {
	DefaultProfile string   `json:"default_profile"`
	Profiles       []string `json:"profiles"`
}

// Show returns the redacted configuration.
func Show(o Ref) (*ShowResult, error) {
	dir := ResolveDir(o.ConfigDir)
	cfg, err := Load(dir)
	if err != nil {
		return nil, err
	}
	profile := o.Profile
	if profile == "" {
		profile = cfg.DefaultProfile
	}
	if _, err := cfg.ResolveProfile(profile); err != nil {
		return nil, err
	}
	return &ShowResult{ConfigDir: dir, Profile: profile, Config: cfg.Redacted()}, nil
}

// Paths reports the resolved locations without requiring an initialised
// workspace — doctor and the docs rely on that.
func Paths(o Ref) *PathsResult {
	dir := ResolveDir(o.ConfigDir)
	source := "OS default"
	switch {
	case o.ConfigDir != "":
		source = "-config-dir flag"
	case envSet(EnvConfigDir):
		source = EnvConfigDir + " environment variable"
	case envSet("XDG_CONFIG_HOME"):
		source = "XDG_CONFIG_HOME"
	}
	return &PathsResult{
		ConfigDir:       dir,
		ConfigFile:      FilePath(dir),
		CredentialsFile: CredentialsPath(dir),
		ProfilesDir:     ProfilesDir(dir),
		Source:          source,
	}
}

// SetSource adds or replaces a data source on a profile.
func SetSource(o Ref, src Source) (*SourceResult, error) {
	if strings.TrimSpace(src.Name) == "" {
		return nil, errors.New("a source name is required (-name)")
	}
	if strings.TrimSpace(src.Endpoint) == "" {
		return nil, errors.New("an endpoint is required (-endpoint)")
	}
	if src.Kind == "" {
		src.Kind = DetectKind(src.Endpoint)
	}
	if err := ValidateKind(src.Kind); err != nil {
		return nil, err
	}

	dir := ResolveDir(o.ConfigDir)
	cfg, err := Load(dir)
	if err != nil {
		return nil, err
	}
	profile, err := cfg.ResolveProfile(o.Profile)
	if err != nil {
		return nil, err
	}
	profile.SetSource(src)
	if err := cfg.Save(dir); err != nil {
		return nil, err
	}

	shown := src
	shown.Endpoint = RedactEndpoint(src.Endpoint)
	return &SourceResult{Profile: profile.Name, Source: shown}, nil
}

// RemoveSource deletes a source from a profile.
func RemoveSource(o Ref, name string) (*SourceResult, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("a source name is required (-name)")
	}
	dir := ResolveDir(o.ConfigDir)
	cfg, err := Load(dir)
	if err != nil {
		return nil, err
	}
	profile, err := cfg.ResolveProfile(o.Profile)
	if err != nil {
		return nil, err
	}
	src, err := profile.Source(name)
	if err != nil {
		return nil, err
	}
	delete(profile.Sources, name)
	if err := cfg.Save(dir); err != nil {
		return nil, err
	}
	src.Endpoint = RedactEndpoint(src.Endpoint)
	return &SourceResult{Profile: profile.Name, Source: src, Removed: true}, nil
}

// UseProfile points the default profile at name.
func UseProfile(o Ref, name string) (*ProfileResult, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("a profile name is required")
	}
	dir := ResolveDir(o.ConfigDir)
	cfg, err := Load(dir)
	if err != nil {
		return nil, err
	}
	if _, err := cfg.ResolveProfile(name); err != nil {
		return nil, err
	}
	cfg.DefaultProfile = name
	if err := cfg.Save(dir); err != nil {
		return nil, err
	}
	return &ProfileResult{DefaultProfile: name, Profiles: cfg.ProfileNames()}, nil
}

// Profiles lists the configured profiles.
func Profiles(o Ref) (*ProfileResult, error) {
	dir := ResolveDir(o.ConfigDir)
	cfg, err := Load(dir)
	if err != nil {
		return nil, err
	}
	return &ProfileResult{DefaultProfile: cfg.DefaultProfile, Profiles: cfg.ProfileNames()}, nil
}

// Supported source kinds. Keep this list and the connect tool in step.
const (
	KindHTTP  = "http"
	KindHTTPS = "https"
	KindTCP   = "tcp"
	// KindPostgres is a database a tool can actually speak to, not merely
	// reach. Reachability checks still treat it as a TCP endpoint; the tools
	// that query it (dataprep-profile) key off this kind.
	KindPostgres = "postgres"
)

// DetectKind guesses the kind from the endpoint shape.
func DetectKind(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme == "" {
		return KindTCP
	}
	switch u.Scheme {
	case "http":
		return KindHTTP
	case "https":
		return KindHTTPS
	case "postgres", "postgresql":
		return KindPostgres
	default:
		// mysql://, redis://, kafka:// ... are reachability-checked over TCP
		// until a tool exists that speaks their protocol.
		return KindTCP
	}
}

// ValidateKind rejects kinds no tool can act on.
func ValidateKind(kind string) error {
	switch kind {
	case KindHTTP, KindHTTPS, KindTCP, KindPostgres:
		return nil
	default:
		return fmt.Errorf("unsupported source kind %q (want %s, %s, %s or %s)",
			kind, KindHTTP, KindHTTPS, KindTCP, KindPostgres)
	}
}

func envSet(key string) bool { return strings.TrimSpace(os.Getenv(key)) != "" }
