package doctor

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/javadbayzavi/dataprep-core/auth"
	"github.com/javadbayzavi/dataprep-core/workspace"
)

func check(t *testing.T, r *Report, name string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("report has no check %q: %+v", name, r.Checks)
	return Check{}
}

func TestUninitializedWorkspaceFailsWithAHint(t *testing.T) {
	r := Run(workspace.Ref{ConfigDir: filepath.Join(t.TempDir(), "missing")})
	if r.Healthy {
		t.Fatal("missing config dir reported as healthy")
	}
	c := check(t, r, "config-dir")
	if c.Status != StatusFail {
		t.Errorf("config-dir status = %q, want fail", c.Status)
	}
	if c.Hint == "" {
		t.Error("failing check carries no hint")
	}
}

func TestFreshWorkspaceIsHealthyWithWarnings(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dataprep")
	if _, err := workspace.Init(workspace.InitOptions{Ref: workspace.Ref{ConfigDir: dir}, CLIVersion: "test"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := auth.NewStore(dir).EnsurePlaceholder(); err != nil {
		t.Fatalf("EnsurePlaceholder: %v", err)
	}

	r := Run(workspace.Ref{ConfigDir: dir})
	if !r.Healthy {
		t.Fatalf("fresh workspace reported unhealthy: %+v", r.Checks)
	}
	// No workspace directory, no source and no credential yet: warnings, not
	// failures — the tools still work.
	for _, name := range []string{"workspace", "sources", "credentials"} {
		if got := check(t, r, name).Status; got != StatusWarn {
			t.Errorf("%s status = %q, want warn", name, got)
		}
	}
}

func TestFullyConfiguredWorkspacePassesEveryCheck(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dataprep")
	ws := filepath.Join(t.TempDir(), "work")
	ref := workspace.Ref{ConfigDir: dir, Profile: "dev"}

	if _, err := workspace.Init(workspace.InitOptions{Ref: ref, Workspace: ws, CLIVersion: "test"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := workspace.SetSource(ref, workspace.Source{Name: "orders", Endpoint: "https://example.com"}); err != nil {
		t.Fatalf("SetSource: %v", err)
	}
	if _, err := auth.Login(auth.LoginOptions{Ref: ref, Provider: "example", APIKey: "TESTKEY"}); err != nil {
		t.Fatalf("Login: %v", err)
	}

	r := Run(ref)
	if !r.Healthy {
		t.Fatalf("configured workspace reported unhealthy: %+v", r.Checks)
	}
	for _, name := range []string{"config-dir", "config-file", "profile", "workspace", "sources", "credentials-file", "credentials"} {
		if got := check(t, r, name).Status; got != StatusOK {
			t.Errorf("%s status = %q, want ok", name, got)
		}
	}
}

func TestUnknownProfileFails(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dataprep")
	if _, err := workspace.Init(workspace.InitOptions{Ref: workspace.Ref{ConfigDir: dir}, CLIVersion: "test"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	r := Run(workspace.Ref{ConfigDir: dir, Profile: "nope"})
	if r.Healthy {
		t.Fatal("unknown profile reported as healthy")
	}
	if got := check(t, r, "profile").Status; got != StatusFail {
		t.Errorf("profile status = %q, want fail", got)
	}
}

// A world-readable credential file is the one permission problem doctor must
// never wave through.
func TestLooseCredentialPermissionsFail(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not apply on Windows")
	}
	dir := filepath.Join(t.TempDir(), "dataprep")
	ref := workspace.Ref{ConfigDir: dir}
	if _, err := workspace.Init(workspace.InitOptions{Ref: ref, CLIVersion: "test"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := auth.Login(auth.LoginOptions{Ref: ref, Provider: "example", APIKey: "TESTKEY"}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := os.Chmod(auth.NewStore(dir).Path(), 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	r := Run(ref)
	if r.Healthy {
		t.Fatal("world-readable credentials reported as healthy")
	}
	c := check(t, r, "credentials-file")
	if c.Status != StatusFail {
		t.Errorf("credentials-file status = %q, want fail", c.Status)
	}
	if c.Hint == "" {
		t.Error("no chmod hint given")
	}
}

func TestDoctorDoesNotMutateTheWorkspace(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dataprep")
	ws := filepath.Join(t.TempDir(), "work")
	ref := workspace.Ref{ConfigDir: dir}
	if _, err := workspace.Init(workspace.InitOptions{Ref: ref, Workspace: ws, CLIVersion: "test"}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	before, err := os.ReadFile(workspace.FilePath(dir))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	Run(ref)

	after, err := os.ReadFile(workspace.FilePath(dir))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(before) != string(after) {
		t.Error("doctor modified config.json")
	}
	// The writability probe must clean up after itself.
	entries, err := os.ReadDir(ws)
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("doctor left %d file(s) in the workspace", len(entries))
	}
}
