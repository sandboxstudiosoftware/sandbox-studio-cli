package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestBaseURLAppendsAPI verifies the "/api" suffix handling.
func TestBaseURLAppendsAPI(t *testing.T) {
	cases := map[string]string{
		"https://x.example.com":      "https://x.example.com/api",
		"https://x.example.com/":     "https://x.example.com/api",
		"https://x.example.com/api":  "https://x.example.com/api",
		"https://x.example.com/api/": "https://x.example.com/api",
	}
	for in, want := range cases {
		if got := New(in, "").baseURL; got != want {
			t.Errorf("New(%q).baseURL = %q, want %q", in, got, want)
		}
	}
}

// TestGetLeaseAccessUnwrapsJSend verifies the JSend envelope is unwrapped and the
// bearer token is attached.
func TestGetLeaseAccessUnwrapsJSend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer jwt123" {
			t.Errorf("missing/wrong auth header: %q", got)
		}
		if r.URL.Path != "/api/leases/L1/access" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","data":{"type":"FEDERATION","consoleUrl":"https://c","credentials":{"accessKeyId":"AK","secretAccessKey":"SK","sessionToken":"ST","expiration":"2030-01-01T00:00:00Z"}}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "jwt123")
	access, err := c.GetLeaseAccess(context.Background(), "L1", "")
	if err != nil {
		t.Fatalf("GetLeaseAccess: %v", err)
	}
	if access.Type != "FEDERATION" || access.Credentials == nil || access.Credentials.AccessKeyID != "AK" {
		t.Fatalf("unexpected access: %+v", access)
	}
}

// TestErrorMappingExtractsMessage verifies non-2xx responses become *APIError with
// a useful message pulled from the JSend/OAuth body.
func TestErrorMappingExtractsMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"status":"fail","data":{"message":"not your lease"}}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "t").GetLease(context.Background(), "L1")
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusForbidden || apiErr.Message != "not your lease" {
		t.Fatalf("unexpected APIError: %+v", apiErr)
	}
}

// TestExchangeTokenNotEnveloped verifies the token endpoint is parsed as a raw
// OAuth2 body (no JSend envelope).
func TestExchangeTokenNotEnveloped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("want form content-type, got %q", ct)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"jwt","token_type":"Bearer","expires_in":86400}`))
	}))
	defer srv.Close()

	tok, err := New(srv.URL, "").ExchangeToken(context.Background(), "id", "secret")
	if err != nil {
		t.Fatalf("ExchangeToken: %v", err)
	}
	if tok.AccessToken != "jwt" || tok.ExpiresIn != 86400 {
		t.Fatalf("unexpected token: %+v", tok)
	}
}
