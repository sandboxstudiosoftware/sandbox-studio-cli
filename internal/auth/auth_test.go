package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/config"
	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/keyring"
)

// makeJWT builds an unsigned-but-well-formed JWT whose exp claim is now+ttl.
func makeJWT(ttl time.Duration) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`))
	payload, _ := json.Marshal(map[string]any{
		"sub":  "user-1",
		"role": "User",
		"exp":  time.Now().Add(ttl).Unix(),
	})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// tokenServer returns an httptest server that serves a fresh token and counts how
// many times the token endpoint was hit.
func tokenServer(t *testing.T, ttl time.Duration, hits *int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/oauth/token" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		atomic.AddInt32(hits, 1)
		resp := map[string]any{"access_token": makeJWT(ttl), "token_type": "Bearer", "expires_in": int(ttl.Seconds())}
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func TestTokenExchangesAndCaches(t *testing.T) {
	// Isolate from any ambient env credentials.
	t.Setenv(config.EnvClientID, "")
	t.Setenv(config.EnvClientSecret, "")

	var hits int32
	srv := tokenServer(t, time.Hour, &hits)
	defer srv.Close()

	kr := keyring.NewMemory()
	_ = kr.Set(KeyClientID, "id")
	_ = kr.Set(KeyClientSecret, "secret")

	a := New(kr, srv.URL)

	// First call exchanges.
	tok1, err := a.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	// Second call must hit the cache, not the server.
	tok2, err := a.Token(context.Background())
	if err != nil {
		t.Fatalf("Token(2): %v", err)
	}
	if tok1 != tok2 {
		t.Error("expected cached token to be reused")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Errorf("token endpoint hits = %d, want 1 (second call should be cached)", got)
	}
}

func TestTokenRefreshesWhenNearExpiry(t *testing.T) {
	t.Setenv(config.EnvClientID, "")
	t.Setenv(config.EnvClientSecret, "")

	var hits int32
	// TTL of 30s is within the 60s refresh window, so the cache must be considered
	// stale and re-exchanged every call.
	srv := tokenServer(t, 30*time.Second, &hits)
	defer srv.Close()

	kr := keyring.NewMemory()
	_ = kr.Set(KeyClientID, "id")
	_ = kr.Set(KeyClientSecret, "secret")

	a := New(kr, srv.URL)
	if _, err := a.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if _, err := a.Token(context.Background()); err != nil {
		t.Fatalf("Token(2): %v", err)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("token endpoint hits = %d, want 2 (near-expiry token must refresh)", got)
	}
}

func TestEnvCredentialsTakePrecedence(t *testing.T) {
	var hits int32
	srv := tokenServer(t, time.Hour, &hits)
	defer srv.Close()

	// Env set; keyring deliberately empty. Exchange must still succeed.
	t.Setenv(config.EnvClientID, "env-id")
	t.Setenv(config.EnvClientSecret, "env-secret")

	a := New(keyring.NewMemory(), srv.URL)
	if _, err := a.Token(context.Background()); err != nil {
		t.Fatalf("Token with env creds: %v", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Error("expected one token exchange using env credentials")
	}
}

func TestMissingCredentialsError(t *testing.T) {
	t.Setenv(config.EnvClientID, "")
	t.Setenv(config.EnvClientSecret, "")

	a := New(keyring.NewMemory(), "https://example.com")
	if _, err := a.Token(context.Background()); err == nil {
		t.Fatal("expected an error when no credentials are configured")
	}
}

func TestJWTExpiryParsing(t *testing.T) {
	tok := makeJWT(2 * time.Hour)
	exp, err := jwtExpiry(tok)
	if err != nil {
		t.Fatalf("jwtExpiry: %v", err)
	}
	if d := time.Until(exp); d < 90*time.Minute || d > 2*time.Hour+time.Minute {
		t.Errorf("exp out of expected range: %s", d)
	}

	for _, bad := range []string{"", "a.b", "a.b.c.d", "not-a-jwt"} {
		if _, err := jwtExpiry(bad); err == nil {
			t.Errorf("jwtExpiry(%q): expected error", bad)
		}
	}
}
