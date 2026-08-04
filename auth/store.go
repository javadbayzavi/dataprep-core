// Package auth stores and uses authentication material for the dataprep tools.
//
// The MVP store is a single owner-readable JSON file next to workspace.json, so
// it works identically on a laptop and inside a container with a mounted
// volume. Secrets are never rendered by String or JSON output helpers: callers
// print Masked values.
package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/javadbayzavi/dataprep-core/workspace"
)

// SchemaVersion of the credentials document.
const SchemaVersion = "1"

// Method describes how a credential was obtained.
const MethodAPIKey = "api-key"

// ErrNotFound is returned when no credential matches.
var ErrNotFound = errors.New("no stored credential")

// Credential is one stored secret plus its metadata.
type Credential struct {
	Provider  string    `json:"provider"`
	Profile   string    `json:"profile"`
	Method    string    `json:"method"`
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
}

// Masked returns a copy whose token is safe to print or log.
func (c Credential) Masked() Credential {
	c.Token = Mask(c.Token)
	return c
}

// Mask reduces a secret to a non-reversible hint.
func Mask(token string) string {
	switch {
	case token == "":
		return ""
	case len(token) <= 4:
		return "****"
	default:
		return "****" + token[len(token)-4:]
	}
}

type document struct {
	SchemaVersion string                `json:"schema_version"`
	Credentials   map[string]Credential `json:"credentials"`
}

// Store is the credential file for one config directory.
type Store struct {
	path string
}

// NewStore binds a store to a config directory.
func NewStore(dir string) *Store {
	return &Store{path: workspace.CredentialsPath(dir)}
}

// Path is the backing file, useful for doctor and error messages.
func (s *Store) Path() string { return s.path }

func key(profile, provider string) string { return profile + "/" + provider }

// EnsurePlaceholder creates an empty credentials file with safe permissions,
// used by init so the file exists (and is owner only) before any secret does.
func (s *Store) EnsurePlaceholder() error {
	if _, err := os.Stat(s.path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", s.path, err)
	}
	return s.save(&document{SchemaVersion: SchemaVersion, Credentials: map[string]Credential{}})
}

func (s *Store) load() (*document, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &document{SchemaVersion: SchemaVersion, Credentials: map[string]Credential{}}, nil
		}
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	if doc.Credentials == nil {
		doc.Credentials = map[string]Credential{}
	}
	return &doc, nil
}

func (s *Store) save(doc *document) error {
	doc.SchemaVersion = SchemaVersion
	if err := os.MkdirAll(filepath.Dir(s.path), workspace.DirPerm); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(s.path), err)
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode credentials: %w", err)
	}
	return writeSecretFile(s.path, append(raw, '\n'))
}

func writeSecretFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	// Tighten before writing, so the secret never exists world readable.
	if err := tmp.Chmod(workspace.FilePerm); err != nil {
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
	return os.Chmod(path, workspace.FilePerm)
}

// Set stores (or replaces) a credential.
func (s *Store) Set(c Credential) error {
	if c.Provider == "" {
		return errors.New("provider must not be empty")
	}
	if c.Profile == "" {
		return errors.New("profile must not be empty")
	}
	if c.Token == "" {
		return errors.New("token must not be empty")
	}
	if c.Method == "" {
		c.Method = MethodAPIKey
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	doc, err := s.load()
	if err != nil {
		return err
	}
	doc.Credentials[key(c.Profile, c.Provider)] = c
	return s.save(doc)
}

// Get returns the credential of a profile/provider pair.
func (s *Store) Get(profile, provider string) (Credential, error) {
	doc, err := s.load()
	if err != nil {
		return Credential{}, err
	}
	c, ok := doc.Credentials[key(profile, provider)]
	if !ok {
		return Credential{}, fmt.Errorf("%w for provider %q in profile %q", ErrNotFound, provider, profile)
	}
	return c, nil
}

// List returns every credential of a profile (all profiles when empty),
// sorted by provider. Tokens are masked: nothing that lists is allowed to leak.
func (s *Store) List(profile string) ([]Credential, error) {
	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]Credential, 0, len(doc.Credentials))
	for _, c := range doc.Credentials {
		if profile != "" && c.Profile != profile {
			continue
		}
		out = append(out, c.Masked())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Profile != out[j].Profile {
			return out[i].Profile < out[j].Profile
		}
		return out[i].Provider < out[j].Provider
	})
	return out, nil
}

// Delete removes one credential. It reports whether something was removed.
func (s *Store) Delete(profile, provider string) (bool, error) {
	doc, err := s.load()
	if err != nil {
		return false, err
	}
	k := key(profile, provider)
	if _, ok := doc.Credentials[k]; !ok {
		return false, nil
	}
	delete(doc.Credentials, k)
	return true, s.save(doc)
}

// DeleteProfile removes every credential of a profile, returning the count.
func (s *Store) DeleteProfile(profile string) (int, error) {
	doc, err := s.load()
	if err != nil {
		return 0, err
	}
	removed := 0
	for k, c := range doc.Credentials {
		if c.Profile == profile {
			delete(doc.Credentials, k)
			removed++
		}
	}
	if removed == 0 {
		return 0, nil
	}
	return removed, s.save(doc)
}

// CheckPermissions reports whether the file is readable by group or others.
// Always false on Windows, where the bits do not apply.
func (s *Store) CheckPermissions() (ok bool, mode os.FileMode, err error) {
	info, err := os.Stat(s.path)
	if err != nil {
		return false, 0, err
	}
	mode = info.Mode().Perm()
	if runtime.GOOS == "windows" {
		return true, mode, nil
	}
	return mode&0o077 == 0, mode, nil
}
