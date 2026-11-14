package auth

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sandboxstudio/sbs-cli/internal/api"
	"github.com/sandboxstudio/sbs-cli/internal/config"
	"github.com/sandboxstudio/sbs-cli/internal/keyring"
)

func noEnvCreds(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvClientID, "")
	t.Setenv(config.EnvClientSecret, "")
}

func TestBaseURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://x.example.com":       "https://x.example.com",
		"https://x.example.com/":      "https://x.example.com",
		"https://x.example.com/api":   "https://x.example.com",
		"https://x.example.com/api/":  "https://x.example.com",
		" https://x.example.com/api ": "https://x.example.com",
	} {
		if got := BaseURL(in); got != want {
			t.Errorf("BaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNoSessionRequiresLogin(t *testing.T) {
	noEnvCreds(t)
	a := New(keyring.NewMemory(), "https://x.example.com")
	if _, err := a.Token(context.Background()); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("err = %v, want ErrLoginRequired", err)
	}
}

func TestStoredSessionIsUsedUntilExpiry(t *testing.T) {
	noEnvCreds(t)
	kr := keyring.NewMemory()
	a := New(kr, "https://x.example.com/")
	exp := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := a.StoreSession(&api.CLISession{Token: "sess", ExpiresAt: "2030-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	a.now = func() time.Time { return exp.Add(-2 * time.Minute) }
	if tok, err := a.Token(context.Background()); err != nil || tok != "sess" {
		t.Fatalf("Token = %q, %v", tok, err)
	}
	// Within the skew of expiry (and after it) the session is expired; there is
	// no refresh.
	for _, now := range []time.Time{exp.Add(-30 * time.Second), exp.Add(time.Hour)} {
		a.now = func() time.Time { return now }
		if _, err := a.Token(context.Background()); !errors.Is(err, ErrSessionExpired) {
			t.Errorf("at %s: err = %v, want ErrSessionExpired", now, err)
		}
		if !a.SessionExpired() {
			t.Error("SessionExpired() should be true")
		}
	}
	if !strings.Contains(ErrSessionExpired.Error(), "sbs login") {
		t.Errorf("expired message should tell the user to run sbs login: %q", ErrSessionExpired)
	}
}

func TestStoreSessionExpiry(t *testing.T) {
	a := New(keyring.NewMemory(), "https://x.example.com")

	// Fractional seconds (JavaScript toISOString) parse.
	s, err := a.StoreSession(&api.CLISession{Token: "t", ExpiresAt: "2030-01-01T00:00:00.000Z"})
	if err != nil || !s.ExpiresAt.Equal(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("session = %+v, %v", s, err)
	}

	// Without expiresAt, the token's exp claim is used.
	jwt := makeJWT(time.Hour)
	s, err = a.StoreSession(&api.CLISession{Token: jwt})
	if err != nil || time.Until(s.ExpiresAt) < 59*time.Minute {
		t.Errorf("fallback to exp: %+v, %v", s, err)
	}

	if _, err := a.StoreSession(&api.CLISession{Token: "opaque", ExpiresAt: "soon"}); err == nil {
		t.Error("an unusable expiry must be an error")
	}
	if _, err := a.StoreSession(&api.CLISession{}); err == nil {
		t.Error("an empty token must be an error")
	}
}

func TestClientCredentialsTakePrecedenceOverSession(t *testing.T) {
	noEnvCreds(t)
	var hits int32
	srv := tokenServer(t, time.Hour, &hits)
	defer srv.Close()

	kr := keyring.NewMemory()
	a := New(kr, srv.URL)
	if _, err := a.StoreSession(&api.CLISession{Token: "sess", ExpiresAt: "2030-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	_ = kr.Set(KeyClientID, "id")
	_ = kr.Set(KeyClientSecret, "secret")

	tok, err := a.Token(context.Background())
	if err != nil || tok == "sess" || atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("Token = %q, %v (hits %d); want the client-credentials JWT", tok, err, hits)
	}
}

func TestMapAccessError(t *testing.T) {
	noEnvCreds(t)
	kr := keyring.NewMemory()
	a := New(kr, "https://x.example.com")
	exp := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	_, _ = a.StoreSession(&api.CLISession{Token: "sess", ExpiresAt: exp.Format(time.RFC3339)})
	a.now = func() time.Time { return exp.Add(-time.Hour) }

	e401 := &api.APIError{StatusCode: 401, Message: "Token expired"}
	if err := a.MapAccessError(e401); !strings.Contains(err.Error(), "sbs login") || !strings.Contains(err.Error(), "Token expired") {
		t.Errorf("401: %v", err)
	}
	e403 := &api.APIError{StatusCode: 403, Message: "Not your lease"}
	if err := a.MapAccessError(e403); err != e403 {
		t.Errorf("403 with a valid session should be returned unchanged, got %v", err)
	}
	a.now = func() time.Time { return exp.Add(time.Minute) }
	if err := a.MapAccessError(e403); !strings.Contains(err.Error(), "sbs login") {
		t.Errorf("403 with an expired session should ask for a login, got %v", err)
	}
	e500 := &api.APIError{StatusCode: 500, Message: "boom"}
	if err := a.MapAccessError(e500); err != e500 {
		t.Errorf("other errors pass through, got %v", err)
	}
}

func TestLogoutClearsSessionButKeepsClient(t *testing.T) {
	kr := keyring.NewMemory()
	a := New(kr, "https://x.example.com")
	_, _ = a.StoreSession(&api.CLISession{Token: "sess", ExpiresAt: "2030-01-01T00:00:00Z"})
	_ = kr.Set(KeyClientID, "id")
	_ = kr.Set(KeyClientSecret, "secret")
	_ = kr.Set("oidc-refresh-token:https://x.example.com", "legacy")

	a.Logout()
	if _, err := a.Session(); !errors.Is(err, ErrLoginRequired) {
		t.Errorf("session should be gone, err = %v", err)
	}
	if _, err := kr.Get("oidc-refresh-token:https://x.example.com"); err == nil {
		t.Error("legacy tokens should be removed")
	}
	if _, err := kr.Get(KeyClientID); err != nil {
		t.Error("client credentials must be kept")
	}
}
