// Package probe checks that a data source is reachable, optionally using
// a stored credential. It is the first real pipeline step: everything later
// (extract, profile, transform) will follow the same shape — resolve source,
// act, return a JSON-serialisable result.
package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/javadbayzavi/dataprep-core/auth"
	"github.com/javadbayzavi/dataprep-core/workspace"
)

// Options are the inputs of Run.
type Options struct {
	workspace.Ref

	// Either a named source from the profile ...
	Source string
	// ... or an ad hoc endpoint.
	Endpoint string
	Kind     string

	Provider string // credential provider to attach (defaults to the source's)
	Method   string // HTTP method, default GET
	Headers  map[string]string
	Timeout  time.Duration
}

// Result is what the tool prints; Endpoint is always redacted.
type Result struct {
	Profile     string `json:"profile"`
	Source      string `json:"source,omitempty"`
	Kind        string `json:"kind"`
	Endpoint    string `json:"endpoint"`
	Reachable   bool   `json:"reachable"`
	StatusCode  int    `json:"status_code,omitempty"`
	LatencyMS   int64  `json:"latency_ms"`
	Authorized  bool   `json:"authorized"` // a credential was attached
	Detail      string `json:"detail"`
	Provider    string `json:"provider,omitempty"`
	MaskedToken string `json:"masked_token,omitempty"`
}

// DefaultTimeout keeps a failing source from hanging a pipeline.
const DefaultTimeout = 10 * time.Second

// ErrUnreachable marks a check that ran correctly but failed to connect, so
// the CLI can exit non-zero without printing a Go error.
var ErrUnreachable = errors.New("source is not reachable")

// Run performs the reachability check.
func Run(opts Options) (*Result, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	if opts.Method == "" {
		opts.Method = http.MethodGet
	}

	endpoint, kind, sourceName, provider, profile, err := resolveTarget(opts)
	if err != nil {
		return nil, err
	}

	res := &Result{
		Profile:   profile,
		Source:    sourceName,
		Kind:      kind,
		Endpoint:  workspace.RedactEndpoint(endpoint),
		Provider:  provider,
		LatencyMS: 0,
	}

	var cred auth.Credential
	if provider != "" {
		c, err := auth.NewStore(workspace.ResolveDir(opts.ConfigDir)).Get(profile, provider)
		if err != nil {
			if !errors.Is(err, auth.ErrNotFound) {
				return nil, err
			}
			res.Detail = fmt.Sprintf("no credential stored for provider %q, connecting anonymously", provider)
		} else {
			cred = c
			res.Authorized = true
			res.MaskedToken = auth.Mask(c.Token)
		}
	}

	start := time.Now()
	switch kind {
	case workspace.KindHTTP, workspace.KindHTTPS:
		status, detail, err := checkHTTP(endpoint, opts, cred)
		res.LatencyMS = time.Since(start).Milliseconds()
		res.StatusCode = status
		if err != nil {
			res.Detail = detail
			return res, fmt.Errorf("%w: %s", ErrUnreachable, err.Error())
		}
		res.Reachable = true
		res.Detail = detail
	case workspace.KindTCP, workspace.KindPostgres:
		detail, err := checkTCP(endpoint, opts.Timeout)
		res.LatencyMS = time.Since(start).Milliseconds()
		if err != nil {
			res.Detail = detail
			return res, fmt.Errorf("%w: %s", ErrUnreachable, err.Error())
		}
		res.Reachable = true
		res.Detail = detail
	default:
		return nil, workspace.ValidateKind(kind)
	}

	return res, nil
}

// resolveTarget settles endpoint/kind/provider from either the named source or
// the ad hoc flags.
func resolveTarget(opts Options) (endpoint, kind, sourceName, provider, profile string, err error) {
	if opts.Source == "" && opts.Endpoint == "" {
		return "", "", "", "", "", errors.New("give a source (-source) or an endpoint (-endpoint)")
	}

	// An ad hoc endpoint does not need an initialised workspace, unless a
	// credential provider was asked for.
	if opts.Source == "" && opts.Provider == "" {
		kind = opts.Kind
		if kind == "" {
			kind = workspace.DetectKind(opts.Endpoint)
		}
		if err := workspace.ValidateKind(kind); err != nil {
			return "", "", "", "", "", err
		}
		return opts.Endpoint, kind, "", "", opts.Profile, nil
	}

	dir := workspace.ResolveDir(opts.ConfigDir)
	cfg, err := workspace.Load(dir)
	if err != nil {
		return "", "", "", "", "", err
	}
	p, err := cfg.ResolveProfile(opts.Profile)
	if err != nil {
		return "", "", "", "", "", err
	}

	endpoint = opts.Endpoint
	kind = opts.Kind
	provider = opts.Provider

	if opts.Source != "" {
		src, err := p.Source(opts.Source)
		if err != nil {
			if len(p.Sources) == 0 {
				return "", "", "", "", "", fmt.Errorf("%w (add one with dataprep-config set-source)", err)
			}
			return "", "", "", "", "", fmt.Errorf("%w (known: %s)", err, strings.Join(p.SourceNames(), ", "))
		}
		sourceName = src.Name
		if endpoint == "" {
			endpoint = src.Endpoint
		}
		if kind == "" {
			kind = src.Kind
		}
		if provider == "" {
			provider = src.Provider
		}
	}
	if kind == "" {
		kind = workspace.DetectKind(endpoint)
	}
	if err := workspace.ValidateKind(kind); err != nil {
		return "", "", "", "", "", err
	}
	return endpoint, kind, sourceName, provider, p.Name, nil
}

func checkHTTP(endpoint string, opts Options, cred auth.Credential) (int, string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opts.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, opts.Method, endpoint, nil)
	if err != nil {
		return 0, "malformed endpoint", err
	}
	req.Header.Set("User-Agent", "dataprep-connect")
	for k, v := range opts.Headers {
		req.Header.Set(k, v)
	}
	if cred.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cred.Token)
	}

	client := &http.Client{Timeout: opts.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "request failed", err
	}
	defer resp.Body.Close()
	// Drain a little so the connection can be reused, and cap it: connect is a
	// health check, not a fetch.
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return resp.StatusCode, "endpoint reachable but rejected the credential",
			fmt.Errorf("HTTP %s", resp.Status)
	}
	if resp.StatusCode >= 500 {
		return resp.StatusCode, "endpoint reachable but unhealthy", fmt.Errorf("HTTP %s", resp.Status)
	}
	return resp.StatusCode, fmt.Sprintf("HTTP %s", resp.Status), nil
}

func checkTCP(endpoint string, timeout time.Duration) (string, error) {
	address, err := tcpAddress(endpoint)
	if err != nil {
		return "malformed endpoint", err
	}
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return "dial failed", err
	}
	defer conn.Close()
	return "tcp connection established to " + address, nil
}

// tcpAddress turns either host:port or a URL into a dialable address.
func tcpAddress(endpoint string) (string, error) {
	if !strings.Contains(endpoint, "://") {
		if _, _, err := net.SplitHostPort(endpoint); err != nil {
			return "", fmt.Errorf("expected host:port, got %q", endpoint)
		}
		return endpoint, nil
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("no host in %q", endpoint)
	}
	port := u.Port()
	if port == "" {
		port = defaultPort(u.Scheme)
		if port == "" {
			return "", fmt.Errorf("no port in %q and no default known for scheme %q", endpoint, u.Scheme)
		}
	}
	return net.JoinHostPort(host, port), nil
}

// defaultPort covers the data sources this MVP is likely to be pointed at.
func defaultPort(scheme string) string {
	switch scheme {
	case "http":
		return "80"
	case "https":
		return "443"
	case "postgres", "postgresql":
		return "5432"
	case "mysql":
		return "3306"
	case "redis":
		return "6379"
	case "mongodb":
		return "27017"
	case "clickhouse":
		return "9000"
	case "kafka":
		return "9092"
	default:
		return ""
	}
}
