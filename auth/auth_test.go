package auth

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/javadbayzavi/dataprep-core/workspace"
)

// newWorkspace gives each test its own initialised config directory.
func newWorkspace(t *testing.T, profile string) workspace.Ref {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "dataprep")
	if _, err := workspace.Init(workspace.InitOptions{
		Ref:        workspace.Ref{ConfigDir: dir, Profile: profile},
		CLIVersion: "test",
	}); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	return workspace.Ref{ConfigDir: dir, Profile: profile}
}

// Acceptance criterion 3 of the roadmap, end to end: login, status, logout.
func TestLoginStatusLogoutRoundTrip(t *testing.T) {
	ref := newWorkspace(t, "ci")

	status, err := Status(ref, "")
	if err != nil {
		t.Fatalf("Status before login: %v", err)
	}
	if status.Authenticated {
		t.Fatal("authenticated before any login")
	}

	login, err := Login(LoginOptions{Ref: ref, Provider: "example", APIKey: "TESTKEY"})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if login.Profile != "ci" || login.Provider != "example" || login.Method != MethodAPIKey {
		t.Fatalf("unexpected login result %+v", login)
	}
	if strings.Contains(login.MaskedToken, "TESTKEY") {
		t.Fatalf("login result leaks the key: %q", login.MaskedToken)
	}

	status, err = Status(ref, "")
	if err != nil {
		t.Fatalf("Status after login: %v", err)
	}
	if !status.Authenticated || len(status.Credentials) != 1 {
		t.Fatalf("status after login = %+v, want authenticated with 1 credential", status)
	}
	if status.Credentials[0].Token == "TESTKEY" {
		t.Fatal("status leaks the raw token")
	}

	out, err := Logout(ref, "example", false)
	if err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if out.Removed != 1 {
		t.Fatalf("removed = %d, want 1", out.Removed)
	}

	status, err = Status(ref, "")
	if err != nil {
		t.Fatalf("Status after logout: %v", err)
	}
	if status.Authenticated {
		t.Fatal("still authenticated after logout")
	}
}

func TestStatusForOneProvider(t *testing.T) {
	ref := newWorkspace(t, "default")
	if _, err := Login(LoginOptions{Ref: ref, Provider: "a", APIKey: "k1"}); err != nil {
		t.Fatalf("Login a: %v", err)
	}
	if _, err := Login(LoginOptions{Ref: ref, Provider: "b", APIKey: "k2"}); err != nil {
		t.Fatalf("Login b: %v", err)
	}

	all, err := Status(ref, "")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(all.Credentials) != 2 {
		t.Fatalf("got %d credentials, want 2", len(all.Credentials))
	}

	one, err := Status(ref, "b")
	if err != nil {
		t.Fatalf("Status(b): %v", err)
	}
	if len(one.Credentials) != 1 || one.Credentials[0].Provider != "b" {
		t.Fatalf("status for b = %+v", one)
	}

	missing, err := Status(ref, "nope")
	if err != nil {
		t.Fatalf("Status(nope): %v", err)
	}
	if missing.Authenticated {
		t.Fatal("unknown provider reported as authenticated")
	}
}

func TestLogoutAllRemovesEveryProvider(t *testing.T) {
	ref := newWorkspace(t, "default")
	for _, p := range []string{"a", "b", "c"} {
		if _, err := Login(LoginOptions{Ref: ref, Provider: p, APIKey: "key-" + p}); err != nil {
			t.Fatalf("Login %s: %v", p, err)
		}
	}
	out, err := Logout(ref, "", true)
	if err != nil {
		t.Fatalf("Logout -all: %v", err)
	}
	if out.Removed != 3 {
		t.Fatalf("removed = %d, want 3", out.Removed)
	}
}

func TestLoginRejectsMissingInput(t *testing.T) {
	ref := newWorkspace(t, "default")
	if _, err := Login(LoginOptions{Ref: ref, APIKey: "k"}); err == nil {
		t.Error("login without a provider succeeded")
	}
	if _, err := Login(LoginOptions{Ref: ref, Provider: "example"}); err == nil {
		t.Error("login without a key succeeded")
	}
}

func TestOperationsRequireAnInitializedWorkspace(t *testing.T) {
	ref := workspace.Ref{ConfigDir: filepath.Join(t.TempDir(), "missing")}
	if _, err := Status(ref, ""); !errors.Is(err, workspace.ErrNotInitialized) {
		t.Fatalf("Status error = %v, want ErrNotInitialized", err)
	}
	if _, err := Login(LoginOptions{Ref: ref, Provider: "p", APIKey: "k"}); !errors.Is(err, workspace.ErrNotInitialized) {
		t.Fatalf("Login error = %v, want ErrNotInitialized", err)
	}
}

// The credential file must never be readable beyond its owner: the roadmap
// makes this a security must-have.
func TestCredentialFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits do not apply on Windows")
	}
	ref := newWorkspace(t, "default")
	if _, err := Login(LoginOptions{Ref: ref, Provider: "example", APIKey: "TESTKEY"}); err != nil {
		t.Fatalf("Login: %v", err)
	}

	store := NewStore(ref.ConfigDir)
	ok, mode, err := store.CheckPermissions()
	if err != nil {
		t.Fatalf("CheckPermissions: %v", err)
	}
	if !ok {
		t.Fatalf("credentials file mode is %#o, want owner-only", mode)
	}

	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != workspace.FilePerm {
		t.Errorf("mode = %#o, want %#o", info.Mode().Perm(), workspace.FilePerm)
	}
}

func TestMask(t *testing.T) {
	cases := map[string]string{
		"":                 "",
		"abc":              "****",
		"abcd":             "****",
		"abcde":            "****bcde",
		"TESTKEY123456":    "****3456",
		"0123456789abcdef": "****cdef",
	}
	for in, want := range cases {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStoreRoundTripAndDelete(t *testing.T) {
	ref := newWorkspace(t, "default")
	store := NewStore(ref.ConfigDir)

	if _, err := store.Get("default", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get on empty store = %v, want ErrNotFound", err)
	}
	if err := store.Set(Credential{Provider: "p", Profile: "default", Token: "secret-token"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := store.Get("default", "p")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Token != "secret-token" {
		t.Errorf("token = %q, want the stored value", got.Token)
	}
	if got.Method != MethodAPIKey {
		t.Errorf("method = %q, want the api-key default", got.Method)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt was not filled in")
	}

	removed, err := store.Delete("default", "p")
	if err != nil || !removed {
		t.Fatalf("Delete = %v, %v", removed, err)
	}
	removed, err = store.Delete("default", "p")
	if err != nil || removed {
		t.Fatalf("second Delete = %v, %v, want false, nil", removed, err)
	}
}
