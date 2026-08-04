// Package workspace owns the on-disk state of the dataprep tools: where it
// lives, what it contains and how it is created. Every tool reads the same
// files, which is what lets separate containers cooperate over one mounted
// volume.
package workspace

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// SchemaVersion of the config document. Bump on breaking changes so tools
// built at different times can refuse politely instead of misreading.
const SchemaVersion = "1"

// ErrNotInitialized is returned when no config file exists yet.
var ErrNotInitialized = errors.New("workspace is not initialized (run dataprep-init)")

// Ref points every tool at the same state: which config directory, which
// profile. Empty fields fall back to the OS default directory and the
// configured default profile.
type Ref struct {
	ConfigDir string
	Profile   string
}

// Metadata records how and when the workspace was created.
type Metadata struct {
	CLIVersion string    `json:"cli_version"`
	CreatedAt  time.Time `json:"created_at"`
	OS         string    `json:"os"`
	Arch       string    `json:"arch"`
}

// Source is a data source a profile knows how to reach.
type Source struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`               // http, https or tcp
	Endpoint string `json:"endpoint"`           // URL or host:port
	Provider string `json:"provider,omitempty"` // credential provider to attach
}

// Profile is one named environment (dev, staging, ci ...).
type Profile struct {
	Name      string            `json:"name"`
	Workspace string            `json:"workspace,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	Sources   map[string]Source `json:"sources,omitempty"`
}

// Config is the whole config.json document.
type Config struct {
	SchemaVersion  string              `json:"schema_version"`
	DefaultProfile string              `json:"default_profile"`
	Profiles       map[string]*Profile `json:"profiles"`
	Metadata       Metadata            `json:"metadata"`
}

// New builds an empty config carrying current build metadata.
func New(cliVersion string) *Config {
	return &Config{
		SchemaVersion: SchemaVersion,
		Profiles:      map[string]*Profile{},
		Metadata: Metadata{
			CLIVersion: cliVersion,
			CreatedAt:  time.Now().UTC(),
			OS:         runtime.GOOS,
			Arch:       runtime.GOARCH,
		},
	}
}

// Load reads config.json from dir.
func Load(dir string) (*Config, error) {
	path := FilePath(dir)
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: no %s", ErrNotInitialized, path)
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if cfg.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("unsupported config schema %q in %s (this build understands %q)",
			cfg.SchemaVersion, path, SchemaVersion)
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]*Profile{}
	}
	return &cfg, nil
}

// Save writes config.json atomically with owner-only permissions.
func (c *Config) Save(dir string) error {
	if err := os.MkdirAll(dir, DirPerm); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return writeFileAtomic(FilePath(dir), append(raw, '\n'), FilePerm)
}

// writeFileAtomic writes through a temp file so a crash cannot truncate an
// existing config.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmpName, path, err)
	}
	return os.Chmod(path, perm)
}

// ResolveProfile returns the requested profile, or the default one when name
// is empty.
func (c *Config) ResolveProfile(name string) (*Profile, error) {
	if name == "" {
		name = c.DefaultProfile
	}
	if name == "" {
		return nil, errors.New("no profile given and no default profile configured")
	}
	p, ok := c.Profiles[name]
	if !ok {
		return nil, fmt.Errorf("unknown profile %q (known: %s)", name, strings.Join(c.ProfileNames(), ", "))
	}
	return p, nil
}

// ProfileNames lists the configured profiles, sorted for stable output.
func (c *Config) ProfileNames() []string {
	names := make([]string, 0, len(c.Profiles))
	for n := range c.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// AddProfile registers a profile, refusing to clobber unless force is set.
func (c *Config) AddProfile(p *Profile, force bool) error {
	if p.Name == "" {
		return errors.New("profile name must not be empty")
	}
	if _, exists := c.Profiles[p.Name]; exists && !force {
		return fmt.Errorf("profile %q already exists (use -force to overwrite)", p.Name)
	}
	if c.Profiles == nil {
		c.Profiles = map[string]*Profile{}
	}
	c.Profiles[p.Name] = p
	if c.DefaultProfile == "" {
		c.DefaultProfile = p.Name
	}
	return nil
}

// SetSource adds or replaces a source on a profile.
func (p *Profile) SetSource(s Source) {
	if p.Sources == nil {
		p.Sources = map[string]Source{}
	}
	p.Sources[s.Name] = s
}

// Source looks a source up by name.
func (p *Profile) Source(name string) (Source, error) {
	s, ok := p.Sources[name]
	if !ok {
		return Source{}, fmt.Errorf("profile %q has no source %q", p.Name, name)
	}
	return s, nil
}

// SourceNames lists the profile's sources, sorted.
func (p *Profile) SourceNames() []string {
	names := make([]string, 0, len(p.Sources))
	for n := range p.Sources {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Redacted returns a deep copy safe to print: any credential embedded in a
// source endpoint (postgres://user:pass@host) is masked.
func (c *Config) Redacted() *Config {
	out := *c
	out.Profiles = make(map[string]*Profile, len(c.Profiles))
	for name, p := range c.Profiles {
		cp := *p
		if p.Sources != nil {
			cp.Sources = make(map[string]Source, len(p.Sources))
			for sn, s := range p.Sources {
				s.Endpoint = RedactEndpoint(s.Endpoint)
				cp.Sources[sn] = s
			}
		}
		out.Profiles[name] = &cp
	}
	return &out
}

// RedactEndpoint masks the password of a URL style endpoint. Endpoints that do
// not parse as URLs are returned unchanged (host:port carries no secret).
func RedactEndpoint(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.User == nil {
		return endpoint
	}
	if _, hasPassword := u.User.Password(); !hasPassword {
		return endpoint
	}
	// Splice the mask into the original string rather than re-encoding the URL:
	// url.UserPassword would percent-escape the stars, and re-encoding could
	// change parts of the endpoint the user typed.
	userinfo := u.User.String()
	name, _, ok := strings.Cut(userinfo, ":")
	if !ok {
		return endpoint
	}
	return strings.Replace(endpoint, userinfo+"@", name+":****@", 1)
}
