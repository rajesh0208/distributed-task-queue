// Package oauth provides OAuth 2.0 provider configurations and helpers for
// Google and GitHub login. It handles the authorization URL generation,
// authorization-code exchange, and user-info fetching for each provider.
package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	goauth2 "golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
	"golang.org/x/oauth2/google"
)

// Provider identifies a supported OAuth 2.0 provider.
type Provider string

const (
	ProviderGoogle Provider = "google"
	ProviderGitHub Provider = "github"
)

// UserInfo holds the normalized user data returned by any provider.
type UserInfo struct {
	ID       string   // provider-scoped unique ID
	Email    string
	Name     string
	Provider Provider
}

// ---------------------------------------------------------------------------
// Provider configuration
// ---------------------------------------------------------------------------

// Config holds the per-provider OAuth2 configs and can exchange codes /
// fetch user info for each supported provider.
type Config struct {
	google *goauth2.Config
	github *goauth2.Config
}

// NewConfig builds a Config from environment variables and the application's
// base URL (used to construct the redirect/callback URLs).
//
// Required env vars:
//
//	GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET
//	GITHUB_CLIENT_ID, GITHUB_CLIENT_SECRET
func NewConfig(baseURL string) *Config {
	return &Config{
		google: &goauth2.Config{
			ClientID:     os.Getenv("GOOGLE_CLIENT_ID"),
			ClientSecret: os.Getenv("GOOGLE_CLIENT_SECRET"),
			RedirectURL:  baseURL + "/api/v1/auth/oauth/google/callback",
			Scopes:       []string{"openid", "email", "profile"},
			Endpoint:     google.Endpoint,
		},
		github: &goauth2.Config{
			ClientID:     os.Getenv("GITHUB_CLIENT_ID"),
			ClientSecret: os.Getenv("GITHUB_CLIENT_SECRET"),
			RedirectURL:  baseURL + "/api/v1/auth/oauth/github/callback",
			Scopes:       []string{"user:email", "read:user"},
			Endpoint:     github.Endpoint,
		},
	}
}

// Get returns the oauth2.Config for the named provider.
func (c *Config) Get(provider Provider) (*goauth2.Config, error) {
	switch provider {
	case ProviderGoogle:
		return c.google, nil
	case ProviderGitHub:
		return c.github, nil
	default:
		return nil, fmt.Errorf("unsupported OAuth provider: %s", provider)
	}
}

// AuthCodeURL returns the provider's consent-screen URL. The caller is
// responsible for storing state for CSRF verification.
func (c *Config) AuthCodeURL(provider Provider, state string) (string, error) {
	cfg, err := c.Get(provider)
	if err != nil {
		return "", err
	}
	return cfg.AuthCodeURL(state, goauth2.AccessTypeOnline), nil
}

// Exchange swaps an authorization code for a token.
func (c *Config) Exchange(ctx context.Context, provider Provider, code string) (*goauth2.Token, error) {
	cfg, err := c.Get(provider)
	if err != nil {
		return nil, err
	}
	return cfg.Exchange(ctx, code)
}

// FetchUserInfo exchanges the token for normalized user information from the
// provider's user-info endpoint.
func (c *Config) FetchUserInfo(ctx context.Context, provider Provider, token *goauth2.Token) (*UserInfo, error) {
	cfg, err := c.Get(provider)
	if err != nil {
		return nil, err
	}
	client := cfg.Client(ctx, token)

	switch provider {
	case ProviderGoogle:
		return fetchGoogleUserInfo(client)
	case ProviderGitHub:
		return fetchGitHubUserInfo(client)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}
}

// fetchGoogleUserInfo calls Google's /oauth2/v3/userinfo endpoint and returns
// the normalized user data. The client already carries the access token via the
// oauth2.Transport so no manual Authorization header is needed.
func fetchGoogleUserInfo(client *http.Client) (*UserInfo, error) {
	resp, err := client.Get("https://www.googleapis.com/oauth2/v3/userinfo")
	if err != nil {
		return nil, fmt.Errorf("google userinfo request failed: %w", err)
	}
	defer resp.Body.Close()

	var info struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("failed to decode google userinfo: %w", err)
	}
	return &UserInfo{ID: info.Sub, Email: info.Email, Name: info.Name, Provider: ProviderGoogle}, nil
}

// fetchGitHubUserInfo calls GitHub's /user endpoint. If the user's primary
// email is not included in the response (GitHub hides it by default when it is
// private), it falls back to fetchGitHubPrimaryEmail via /user/emails.
func fetchGitHubUserInfo(client *http.Client) (*UserInfo, error) {
	resp, err := client.Get("https://api.github.com/user")
	if err != nil {
		return nil, fmt.Errorf("github user request failed: %w", err)
	}
	defer resp.Body.Close()

	var info struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("failed to decode github user: %w", err)
	}

	email := info.Email
	if email == "" {
		// GitHub may hide the primary email; fetch it from the emails endpoint.
		email, _ = fetchGitHubPrimaryEmail(client)
	}

	name := info.Name
	if name == "" {
		name = info.Login
	}

	return &UserInfo{
		ID:       fmt.Sprintf("%d", info.ID),
		Email:    email,
		Name:     name,
		Provider: ProviderGitHub,
	}, nil
}

// fetchGitHubPrimaryEmail calls GitHub's /user/emails endpoint and returns the
// address marked as primary, or the first address in the list if none is marked.
func fetchGitHubPrimaryEmail(client *http.Client) (string, error) {
	resp, err := client.Get("https://api.github.com/user/emails")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var emails []struct {
		Email   string `json:"email"`
		Primary bool   `json:"primary"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return "", err
	}
	for _, e := range emails {
		if e.Primary {
			return e.Email, nil
		}
	}
	if len(emails) > 0 {
		return emails[0].Email, nil
	}
	return "", fmt.Errorf("no email found")
}

// ---------------------------------------------------------------------------
// State store — CSRF protection for the OAuth round-trip
// ---------------------------------------------------------------------------

// StateStore saves and verifies the random state parameter used to prevent
// CSRF attacks during the OAuth callback.
type StateStore interface {
	// Save persists a (state → provider) mapping for the given TTL.
	Save(ctx context.Context, state, provider string, ttl time.Duration) error
	// Verify checks that the state was previously saved for the given provider
	// and deletes it. Returns an error if the state is missing or mismatched.
	Verify(ctx context.Context, state, provider string) error
}

// MemoryStateStore is a thread-safe in-memory StateStore used in tests and
// single-instance deployments.
type MemoryStateStore struct {
	mu      sync.Mutex
	entries map[string]memEntry
}

type memEntry struct {
	provider  string
	expiresAt time.Time
}

// NewMemoryStateStore returns an empty MemoryStateStore ready for use.
func NewMemoryStateStore() *MemoryStateStore {
	return &MemoryStateStore{entries: make(map[string]memEntry)}
}

// Save stores a (state → provider) mapping that expires after ttl.
func (m *MemoryStateStore) Save(_ context.Context, state, provider string, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[state] = memEntry{provider: provider, expiresAt: time.Now().Add(ttl)}
	return nil
}

// Verify checks that state was previously saved for the given provider and has
// not expired, then deletes it (one-time use). Returns an error on mismatch or expiry.
func (m *MemoryStateStore) Verify(_ context.Context, state, provider string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[state]
	if !ok || time.Now().After(e.expiresAt) {
		delete(m.entries, state)
		return fmt.Errorf("oauth state not found or expired")
	}
	if e.provider != provider {
		delete(m.entries, state)
		return fmt.Errorf("oauth state provider mismatch")
	}
	delete(m.entries, state)
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// GenerateState returns a cryptographically random URL-safe string suitable
// for use as an OAuth state parameter.
func GenerateState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
