// Login / status / logout against the credential store. None of these return a
// raw token: results carry masked values only.
package auth

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/javadbayzavi/dataprep-core/workspace"
)

// LoginOptions is what login needs on top of a workspace reference.
type LoginOptions struct {
	workspace.Ref
	Provider string
	APIKey   string
}

// LoginResult is printed after a successful login.
type LoginResult struct {
	Profile     string    `json:"profile"`
	Provider    string    `json:"provider"`
	Method      string    `json:"method"`
	MaskedToken string    `json:"masked_token"`
	CreatedAt   time.Time `json:"created_at"`
	StoredIn    string    `json:"stored_in"`
}

// StatusResult answers "am I authenticated?".
type StatusResult struct {
	Profile       string       `json:"profile"`
	Authenticated bool         `json:"authenticated"`
	Credentials   []Credential `json:"credentials"`
}

// LogoutResult reports what was removed.
type LogoutResult struct {
	Profile  string `json:"profile"`
	Provider string `json:"provider,omitempty"`
	Removed  int    `json:"removed"`
}

// profileName loads the config and settles which config dir and profile to
// act on. Every operation here starts with it, so an uninitialised workspace
// fails with one clear message.
func profileName(o workspace.Ref) (string, string, error) {
	dir := workspace.ResolveDir(o.ConfigDir)
	cfg, err := workspace.Load(dir)
	if err != nil {
		return dir, "", err
	}
	p, err := cfg.ResolveProfile(o.Profile)
	if err != nil {
		return dir, "", err
	}
	return dir, p.Name, nil
}

// Login validates and stores an API key for a provider.
func Login(opts LoginOptions) (*LoginResult, error) {
	if strings.TrimSpace(opts.Provider) == "" {
		return nil, errors.New("a provider is required (-provider)")
	}
	key := strings.TrimSpace(opts.APIKey)
	if key == "" {
		return nil, errors.New("an API key is required (-api-key, -api-key-file, DATAPREP_API_KEY or -interactive)")
	}

	dir, profile, err := profileName(opts.Ref)
	if err != nil {
		return nil, err
	}

	cred := Credential{
		Provider:  opts.Provider,
		Profile:   profile,
		Method:    MethodAPIKey,
		Token:     key,
		CreatedAt: time.Now().UTC(),
	}
	store := NewStore(dir)
	if err := store.Set(cred); err != nil {
		return nil, err
	}

	return &LoginResult{
		Profile:     profile,
		Provider:    cred.Provider,
		Method:      cred.Method,
		MaskedToken: Mask(key),
		CreatedAt:   cred.CreatedAt,
		StoredIn:    store.Path(),
	}, nil
}

// Status reports the stored credentials of a profile, optionally narrowed to
// one provider.
func Status(opts workspace.Ref, provider string) (*StatusResult, error) {
	dir, profile, err := profileName(opts)
	if err != nil {
		return nil, err
	}
	store := NewStore(dir)

	if provider != "" {
		cred, err := store.Get(profile, provider)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return &StatusResult{Profile: profile, Authenticated: false, Credentials: []Credential{}}, nil
			}
			return nil, err
		}
		return &StatusResult{
			Profile:       profile,
			Authenticated: true,
			Credentials:   []Credential{cred.Masked()},
		}, nil
	}

	creds, err := store.List(profile)
	if err != nil {
		return nil, err
	}
	return &StatusResult{
		Profile:       profile,
		Authenticated: len(creds) > 0,
		Credentials:   creds,
	}, nil
}

// Logout removes one provider's credential, or all of them for the profile.
func Logout(opts workspace.Ref, provider string, all bool) (*LogoutResult, error) {
	dir, profile, err := profileName(opts)
	if err != nil {
		return nil, err
	}
	store := NewStore(dir)

	if all {
		removed, err := store.DeleteProfile(profile)
		if err != nil {
			return nil, err
		}
		return &LogoutResult{Profile: profile, Removed: removed}, nil
	}
	if provider == "" {
		return nil, errors.New("a provider is required (-provider), or use -all")
	}
	removed, err := store.Delete(profile, provider)
	if err != nil {
		return nil, err
	}
	count := 0
	if removed {
		count = 1
	}
	return &LogoutResult{Profile: profile, Provider: provider, Removed: count}, nil
}

// Token returns the raw secret for a provider. It exists for the connect tool,
// which must actually send the credential; nothing else should call it.
func Token(configDir, profile, provider string) (Credential, error) {
	dir := workspace.ResolveDir(configDir)
	cred, err := NewStore(dir).Get(profile, provider)
	if err != nil {
		return Credential{}, fmt.Errorf("%w", err)
	}
	return cred, nil
}
