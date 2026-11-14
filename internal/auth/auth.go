// Package auth resolves an authenticated API client from stored credentials.
//
// Two credential sources are supported:
//
//   - An API session obtained by `sbs login` (see login.go): the branded browser
//     login yields a Cognito id_token, which is immediately exchanged for an API
//     session at POST /api/auth/token-exchange (client "cli"). Only the session
//     token, its expiry and the deployment URL are stored in the OS keyring; the
//     id_token is discarded. There is no refresh: once the session expires the user must run
//     `sbs login` again.
//   - An API client (client id/secret) from the keyring or environment, for CI.
//     It is exchanged for a JWT via the OAuth client_credentials grant and the JWT
//     is cached until shortly before it expires. When configured, it takes
//     precedence over the login session.
package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sandboxstudio/sbs-cli/internal/api"
	"github.com/sandboxstudio/sbs-cli/internal/config"
	"github.com/sandboxstudio/sbs-cli/internal/keyring"
)

// Keyring keys.
const (
	KeyClientID     = "client-id"
	KeyClientSecret = "client-secret"
	keyTokenPrefix  = "token:" // client-credentials JWT cache, per URL
	// keySessionPrefix holds the API session from `sbs login`, per URL.
	keySessionPrefix = "session:"
)

// legacyKeyPrefixes are keyring entries written by earlier CLI versions (cached
// Cognito id/refresh tokens). They are removed on login and logout so no Cognito
// token lingers in the keyring.
var legacyKeyPrefixes = []string{"oidc-id-token:", "oidc-refresh-token:"}

// sessionSkew is how close to expiry a session (or cached JWT) is treated as
// already expired, so a request does not race the expiry.
const sessionSkew = time.Minute

var (
	// ErrLoginRequired means there is no stored session for the deployment.
	ErrLoginRequired = errors.New("not logged in: run `sbs login`")
	// ErrSessionExpired means the stored session has expired. There is no
	// refresh flow; the user must log in again.
	ErrSessionExpired = errors.New("your Sandbox Studio session has expired: run `sbs login`")
)

// Session is the API session stored in the keyring after `sbs login`.
type Session struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
	URL       string    `json:"url"`
}

// Expired reports whether the session is expired (or within sessionSkew of it).
func (s Session) Expired(now time.Time) bool {
	return !now.Add(sessionSkew).Before(s.ExpiresAt)
}

// Authenticator produces authenticated API clients.
type Authenticator struct {
	kr  keyring.Keyring
	url string // normalised deployment base URL (no trailing slash or /api)
	raw string // URL as configured; used only to clean up legacy keys

	// now is the clock; a field so tests can control expiry.
	now func() time.Time
}

// New returns an Authenticator for the given deployment URL.
func New(kr keyring.Keyring, url string) *Authenticator {
	return &Authenticator{kr: kr, url: BaseURL(url), raw: url, now: time.Now}
}

// URL returns the normalised deployment base URL.
func (a *Authenticator) URL() string { return a.url }

// BaseURL normalises a configured deployment URL to the base that browser paths
// (e.g. the login page) hang off: no trailing slash and no "/api" suffix.
func BaseURL(u string) string {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	u = strings.TrimSuffix(u, "/api")
	return strings.TrimRight(u, "/")
}

// credentials returns the client id/secret, preferring environment overrides.
func (a *Authenticator) credentials() (id, secret string, err error) {
	id = os.Getenv(config.EnvClientID)
	secret = os.Getenv(config.EnvClientSecret)
	if id != "" && secret != "" {
		return id, secret, nil
	}
	if id, err = a.kr.Get(KeyClientID); err != nil {
		return "", "", ErrLoginRequired
	}
	if secret, err = a.kr.Get(KeyClientSecret); err != nil {
		return "", "", ErrLoginRequired
	}
	return id, secret, nil
}

// UsesClientCredentials reports whether API client credentials (environment or
// keyring) are configured for this deployment.
func (a *Authenticator) UsesClientCredentials() bool {
	_, _, err := a.credentials()
	return err == nil
}

// Token returns a bearer token for the API: the client-credentials JWT when an API
// client is configured, otherwise the stored login session. It never opens a
// browser; without a valid session it returns ErrLoginRequired or
// ErrSessionExpired.
func (a *Authenticator) Token(ctx context.Context) (string, error) {
	if !a.UsesClientCredentials() {
		s, err := a.Session()
		if err != nil {
			return "", err
		}
		return s.Token, nil
	}
	if tok := a.cachedToken(keyTokenPrefix + a.url); tok != "" {
		return tok, nil
	}
	id, secret, err := a.credentials()
	if err != nil {
		return "", err
	}
	resp, err := api.New(a.url, "").ExchangeToken(ctx, id, secret)
	if err != nil {
		return "", err
	}
	_ = a.kr.Set(keyTokenPrefix+a.url, resp.AccessToken) // best-effort cache
	return resp.AccessToken, nil
}

// Session returns the stored login session, or ErrLoginRequired /
// ErrSessionExpired.
func (a *Authenticator) Session() (Session, error) {
	raw, err := a.kr.Get(keySessionPrefix + a.url)
	if err != nil || raw == "" {
		return Session{}, ErrLoginRequired
	}
	var s Session
	if err := json.Unmarshal([]byte(raw), &s); err != nil || s.Token == "" {
		return Session{}, ErrLoginRequired
	}
	if s.Expired(a.now()) {
		return Session{}, ErrSessionExpired
	}
	return s, nil
}

// SessionExpired reports whether a session is stored but has expired. Used to
// decide whether a 401/403 should be reported as "log in again".
func (a *Authenticator) SessionExpired() bool {
	_, err := a.Session()
	return errors.Is(err, ErrSessionExpired)
}

// StoreSession validates an exchange response and stores it as the session for
// this deployment. Only the session token, its expiry and the URL are kept.
func (a *Authenticator) StoreSession(cs *api.CLISession) (Session, error) {
	if cs == nil || cs.Token == "" {
		return Session{}, errors.New("the session exchange returned no token")
	}
	exp, err := sessionExpiry(cs)
	if err != nil {
		return Session{}, err
	}
	s := Session{Token: cs.Token, ExpiresAt: exp.UTC(), URL: a.url}
	b, err := json.Marshal(s)
	if err != nil {
		return Session{}, err
	}
	if err := a.kr.Set(keySessionPrefix+a.url, string(b)); err != nil {
		return Session{}, fmt.Errorf("storing the session in the OS keyring: %w", err)
	}
	a.deleteLegacyKeys()
	return s, nil
}

// sessionExpiry reads expiresAt (ISO-8601), falling back to the token's exp claim.
func sessionExpiry(cs *api.CLISession) (time.Time, error) {
	if cs.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, cs.ExpiresAt); err == nil {
			return t, nil
		}
	}
	if t, err := jwtExpiry(cs.Token); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("the session exchange returned an invalid expiresAt %q", cs.ExpiresAt)
}

// MapAccessError turns an authentication failure from the API into an actionable
// error. A 401, or a 403 while the stored session has expired, means the user
// must log in again (unless an API client is in use, whose JWT is simply
// dropped so the next call re-exchanges it). Any other error is returned as is,
// so the server's message is shown.
func (a *Authenticator) MapAccessError(err error) error {
	var ae *api.APIError
	if !errors.As(err, &ae) || (ae.StatusCode != 401 && ae.StatusCode != 403) {
		return err
	}
	if a.UsesClientCredentials() {
		if ae.StatusCode == 401 {
			_ = a.kr.Delete(keyTokenPrefix + a.url)
		}
		return err
	}
	if ae.StatusCode == 401 || a.SessionExpired() {
		msg := ae.Message
		if msg == "" {
			msg = "session rejected"
		}
		return fmt.Errorf("%s: your Sandbox Studio session is no longer valid; run `sbs login`", msg)
	}
	return err
}

// Logout forgets the login session and any cached client-credentials JWT for the
// deployment. Stored client credentials are kept.
func (a *Authenticator) Logout() {
	_ = a.kr.Delete(keySessionPrefix + a.url)
	_ = a.kr.Delete(keyTokenPrefix + a.url)
	a.deleteLegacyKeys()
}

func (a *Authenticator) deleteLegacyKeys() {
	for _, p := range legacyKeyPrefixes {
		_ = a.kr.Delete(p + a.url)
		if a.raw != a.url {
			_ = a.kr.Delete(p + a.raw)
		}
	}
}

// ForgetClientCredentials removes the stored API client id and secret.
func (a *Authenticator) ForgetClientCredentials() {
	_ = a.kr.Delete(KeyClientID)
	_ = a.kr.Delete(KeyClientSecret)
}

// Client returns an authenticated API client.
func (a *Authenticator) Client(ctx context.Context) (*api.Client, error) {
	tok, err := a.Token(ctx)
	if err != nil {
		return nil, err
	}
	return api.New(a.url, tok), nil
}

// cachedToken returns a cached JWT if present and not within sessionSkew of
// expiry.
func (a *Authenticator) cachedToken(key string) string {
	tok, err := a.kr.Get(key)
	if err != nil {
		return ""
	}
	exp, err := jwtExpiry(tok)
	if err != nil || a.now().Add(sessionSkew).After(exp) {
		return ""
	}
	return tok
}

// jwtExpiry extracts the "exp" claim from a JWT without verifying the signature.
// Verification is the server's job; the CLI only needs the expiry.
func jwtExpiry(tok string) (time.Time, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return time.Time{}, fmt.Errorf("malformed token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, err
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, err
	}
	if claims.Exp == 0 {
		return time.Time{}, fmt.Errorf("no exp claim")
	}
	return time.Unix(claims.Exp, 0), nil
}
