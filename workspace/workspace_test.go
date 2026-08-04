package workspace

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDefaultDirPrefersExplicitEnv(t *testing.T) {
	t.Setenv(EnvConfigDir, "/somewhere/dataprep")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	if got := DefaultDir(); got != "/somewhere/dataprep" {
		t.Fatalf("DefaultDir() = %q, want the %s override", got, EnvConfigDir)
	}
}

func TestDefaultDirFollowsXDG(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("XDG does not apply on Windows")
	}
	t.Setenv(EnvConfigDir, "")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")
	want := filepath.Join("/xdg", AppDirName)
	if got := DefaultDir(); got != want {
		t.Fatalf("DefaultDir() = %q, want %q", got, want)
	}
}

func TestDefaultDirFallsBackToHomeConfig(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("~/.config does not apply on Windows")
	}
	t.Setenv(EnvConfigDir, "")
	t.Setenv("XDG_CONFIG_HOME", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	want := filepath.Join(home, ".config", AppDirName)
	if got := DefaultDir(); got != want {
		t.Fatalf("DefaultDir() = %q, want %q", got, want)
	}
}

// Acceptance criterion 2 of the roadmap: init creates the config directory and
// a config file containing the profile, with permissions that disallow
// world-read.
func TestInitCreatesWorkspace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dataprep")
	ws := filepath.Join(t.TempDir(), "work")

	res, err := Init(InitOptions{
		Ref:        Ref{ConfigDir: dir, Profile: "dev"},
		Workspace:  ws,
		CLIVersion: "test",
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	if res.Profile != "dev" || res.DefaultProfile != "dev" {
		t.Fatalf("profile = %q, default = %q, want dev/dev", res.Profile, res.DefaultProfile)
	}
	if res.AlreadyExisted {
		t.Error("AlreadyExisted = true on a fresh directory")
	}
	if !res.CreatedWorkspace {
		t.Error("CreatedWorkspace = false, want the workspace to be created")
	}

	raw, err := os.ReadFile(FilePath(dir))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config is not valid JSON: %v", err)
	}
	if _, ok := cfg.Profiles["dev"]; !ok {
		t.Fatalf("config has no profile dev: %s", raw)
	}
	if cfg.Metadata.CLIVersion != "test" {
		t.Errorf("metadata cli_version = %q, want test", cfg.Metadata.CLIVersion)
	}

	if runtime.GOOS != "windows" {
		assertPerm(t, dir, DirPerm)
		assertPerm(t, FilePath(dir), FilePerm)
		assertPerm(t, ProfilesDir(dir), DirPerm)
	}
}

func TestInitIsIdempotentAcrossProfiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dataprep")

	if _, err := Init(InitOptions{Ref: Ref{ConfigDir: dir, Profile: "dev"}}); err != nil {
		t.Fatalf("first Init: %v", err)
	}
	res, err := Init(InitOptions{Ref: Ref{ConfigDir: dir, Profile: "ci"}})
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if !res.AlreadyExisted {
		t.Error("AlreadyExisted = false on an existing workspace")
	}
	if res.DefaultProfile != "dev" {
		t.Errorf("default profile = %q, want it to stay dev", res.DefaultProfile)
	}

	// Re-initialising the same profile must refuse without -force ...
	if _, err := Init(InitOptions{Ref: Ref{ConfigDir: dir, Profile: "dev"}}); err == nil {
		t.Error("re-init of an existing profile succeeded, want an error")
	}
	// ... and keep the sources with it.
	if _, err := SetSource(Ref{ConfigDir: dir, Profile: "dev"},
		Source{Name: "orders", Endpoint: "https://example.com"}); err != nil {
		t.Fatalf("SetSource: %v", err)
	}
	if _, err := Init(InitOptions{Ref: Ref{ConfigDir: dir, Profile: "dev"}, Force: true}); err != nil {
		t.Fatalf("forced Init: %v", err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := cfg.Profiles["dev"].Source("orders"); err != nil {
		t.Errorf("forced re-init dropped the sources: %v", err)
	}
}

func TestLoadReportsNotInitialized(t *testing.T) {
	if _, err := Load(t.TempDir()); err == nil {
		t.Fatal("Load on an empty directory returned no error")
	} else if !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("Load error = %v, want ErrNotInitialized", err)
	}
}

func TestSetSourceDetectsKind(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dataprep")
	if _, err := Init(InitOptions{Ref: Ref{ConfigDir: dir}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	ref := Ref{ConfigDir: dir}

	cases := []struct {
		endpoint string
		want     string
	}{
		{"https://api.example.com", KindHTTPS},
		{"http://api.example.com", KindHTTP},
		{"db.internal:5432", KindTCP},
		{"postgres://db.internal:5432/orders", KindTCP},
	}
	for _, tc := range cases {
		res, err := SetSource(ref, Source{Name: "s", Endpoint: tc.endpoint})
		if err != nil {
			t.Fatalf("SetSource(%s): %v", tc.endpoint, err)
		}
		if res.Source.Kind != tc.want {
			t.Errorf("kind of %s = %q, want %q", tc.endpoint, res.Source.Kind, tc.want)
		}
	}
}

// Secrets embedded in an endpoint must not survive into printable output.
func TestRedactEndpoint(t *testing.T) {
	cases := map[string]string{
		"postgres://user:s3cret@db:5432/orders": "postgres://user:****@db:5432/orders",
		"postgres://user@db:5432/orders":        "postgres://user@db:5432/orders",
		"https://api.example.com/health":        "https://api.example.com/health",
		"db.internal:5432":                      "db.internal:5432",
	}
	for in, want := range cases {
		if got := RedactEndpoint(in); got != want {
			t.Errorf("RedactEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactedConfigDoesNotMutateOriginal(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dataprep")
	if _, err := Init(InitOptions{Ref: Ref{ConfigDir: dir}}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	ref := Ref{ConfigDir: dir}
	if _, err := SetSource(ref, Source{Name: "db", Endpoint: "postgres://user:s3cret@db:5432/orders"}); err != nil {
		t.Fatalf("SetSource: %v", err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	red := cfg.Redacted()
	if got := red.Profiles[DefaultProfileName].Sources["db"].Endpoint; got != "postgres://user:****@db:5432/orders" {
		t.Errorf("redacted endpoint = %q", got)
	}
	if got := cfg.Profiles[DefaultProfileName].Sources["db"].Endpoint; got != "postgres://user:s3cret@db:5432/orders" {
		t.Errorf("Redacted() mutated the original config: %q", got)
	}
}

func assertPerm(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Errorf("%s has mode %#o, want %#o", path, got, want)
	}
}
