package probe

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/javadbayzavi/dataprep-core/auth"
	"github.com/javadbayzavi/dataprep-core/workspace"
)

func newWorkspace(t *testing.T) workspace.Ref {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "dataprep")
	if _, err := workspace.Init(workspace.InitOptions{
		Ref:        workspace.Ref{ConfigDir: dir, Profile: "test"},
		CLIVersion: "test",
	}); err != nil {
		t.Fatalf("init workspace: %v", err)
	}
	return workspace.Ref{ConfigDir: dir, Profile: "test"}
}

func TestAdHocHTTPEndpointNeedsNoWorkspace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	res, err := Run(Options{Endpoint: srv.URL})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Reachable || res.StatusCode != http.StatusOK {
		t.Fatalf("result = %+v, want reachable with 200", res)
	}
	if res.Kind != workspace.KindHTTP {
		t.Errorf("kind = %q, want %q", res.Kind, workspace.KindHTTP)
	}
}

func TestNamedSourceSendsStoredCredential(t *testing.T) {
	var seen string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ref := newWorkspace(t)
	if _, err := auth.Login(auth.LoginOptions{Ref: ref, Provider: "example", APIKey: "TESTKEY"}); err != nil {
		t.Fatalf("Login: %v", err)
	}
	if _, err := workspace.SetSource(ref, workspace.Source{
		Name: "orders", Endpoint: srv.URL, Provider: "example",
	}); err != nil {
		t.Fatalf("SetSource: %v", err)
	}

	res, err := Run(Options{Ref: ref, Source: "orders"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if seen != "Bearer TESTKEY" {
		t.Errorf("Authorization header = %q, want the stored key as a bearer token", seen)
	}
	if !res.Authorized {
		t.Error("result does not report the credential as attached")
	}
	if res.MaskedToken == "TESTKEY" {
		t.Error("result leaks the raw token")
	}
}

func TestRejectedCredentialIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	res, err := Run(Options{Endpoint: srv.URL})
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("error = %v, want ErrUnreachable", err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", res.StatusCode)
	}
}

func TestServerErrorIsUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := Run(Options{Endpoint: srv.URL}); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("error = %v, want ErrUnreachable", err)
	}
}

func TestTCPSource(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	res, err := Run(Options{Endpoint: ln.Addr().String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !res.Reachable || res.Kind != workspace.KindTCP {
		t.Fatalf("result = %+v, want a reachable tcp check", res)
	}
}

func TestClosedPortIsUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing is listening any more

	res, err := Run(Options{Endpoint: addr, Kind: workspace.KindTCP, Timeout: time.Second})
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("error = %v, want ErrUnreachable", err)
	}
	if res.Reachable {
		t.Error("closed port reported as reachable")
	}
}

func TestRunRequiresATarget(t *testing.T) {
	if _, err := Run(Options{}); err == nil {
		t.Fatal("Run without a source or endpoint succeeded")
	}
}

func TestUnknownSourceMentionsTheKnownOnes(t *testing.T) {
	ref := newWorkspace(t)
	if _, err := workspace.SetSource(ref, workspace.Source{Name: "orders", Endpoint: "https://example.com"}); err != nil {
		t.Fatalf("SetSource: %v", err)
	}
	_, err := Run(Options{Ref: ref, Source: "invoices"})
	if err == nil {
		t.Fatal("unknown source succeeded")
	}
	if got := err.Error(); !strings.Contains(got, "orders") {
		t.Errorf("error %q does not list the known sources", got)
	}
}

func TestTCPAddress(t *testing.T) {
	cases := map[string]string{
		"db.internal:5432":                    "db.internal:5432",
		"postgres://user@db.internal/orders":  "db.internal:5432",
		"mysql://db.internal/orders":          "db.internal:3306",
		"redis://cache.internal:6380":         "cache.internal:6380",
		"https://api.example.com/health":      "api.example.com:443",
		"mongodb://mongo.internal/orders?x=1": "mongo.internal:27017",
	}
	for in, want := range cases {
		got, err := tcpAddress(in)
		if err != nil {
			t.Errorf("tcpAddress(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("tcpAddress(%q) = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"db.internal", "amqp://broker/queue"} {
		if _, err := tcpAddress(bad); err == nil {
			t.Errorf("tcpAddress(%q) succeeded, want an error", bad)
		}
	}
}
