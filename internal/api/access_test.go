package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func shortRetryDelay(t *testing.T) {
	t.Helper()
	orig := AccessRetryDelay
	AccessRetryDelay = time.Millisecond
	t.Cleanup(func() { AccessRetryDelay = orig })
}

const provisioningBody = `{"status":"error","message":"Access to this sandbox is still being provisioned","code":"FEDERATED_ACCESS_PROVISIONING"}`

// accessServer replies 503 FEDERATED_ACCESS_PROVISIONING for the first `fails`
// calls, then succeeds.
func accessServer(t *testing.T, fails int32, hits *int32) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/leases/L1/access" ||
			r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer sess" {
			t.Errorf("unexpected request %s %s %v", r.Method, r.URL.Path, r.Header)
		}
		if atomic.AddInt32(hits, 1) <= fails {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(provisioningBody))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"type":"FEDERATION",` +
			`"consoleUrl":"https://signin.aws.amazon.com/federation?Action=login&SigninToken=x",` +
			`"credentials":{"accessKeyId":"AK","secretAccessKey":"SK","sessionToken":"ST","expiration":"2030-01-01T12:00:00Z"}}}`))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "sess")
}

func TestGetLeaseAccessParsesResponse(t *testing.T) {
	var hits int32
	a, err := accessServer(t, 0, &hits).GetLeaseAccess(context.Background(), "L1", AccessRoleUser)
	if err != nil {
		t.Fatalf("GetLeaseAccess: %v", err)
	}
	if a.Type != "FEDERATION" || !strings.HasPrefix(a.ConsoleURL, "https://signin.aws.amazon.com/federation?Action=login") {
		t.Errorf("access = %+v", a)
	}
	c := a.Credentials
	if c == nil || c.AccessKeyID != "AK" || c.SecretAccessKey != "SK" || c.SessionToken != "ST" || c.Expiration != "2030-01-01T12:00:00Z" {
		t.Errorf("credentials = %+v", c)
	}
}

func TestGetLeaseAccessRetriesProvisioning(t *testing.T) {
	shortRetryDelay(t)
	var hits int32
	if _, err := accessServer(t, 3, &hits).GetLeaseAccess(context.Background(), "L1", ""); err != nil {
		t.Fatalf("GetLeaseAccess after 3 provisioning replies: %v", err)
	}
	if hits != 4 {
		t.Errorf("hits = %d, want 4", hits)
	}
}

func TestGetLeaseAccessGivesUpAfterThreeRetries(t *testing.T) {
	shortRetryDelay(t)
	var hits int32
	_, err := accessServer(t, 100, &hits).GetLeaseAccess(context.Background(), "L1", "")
	if !IsProvisioning(err) || !strings.Contains(err.Error(), "still being provisioned") {
		t.Fatalf("err = %v, want the provisioning error with its message", err)
	}
	if hits != 4 {
		t.Errorf("hits = %d, want 4 (1 + 3 retries)", hits)
	}
}

func TestGetLeaseAccessRetryDelay(t *testing.T) {
	if AccessRetries != 3 || AccessRetryDelay != 3*time.Second {
		t.Errorf("retry policy = %d x %s, want 3 x 3s", AccessRetries, AccessRetryDelay)
	}
	orig := AccessRetryDelay
	AccessRetryDelay = time.Hour
	t.Cleanup(func() { AccessRetryDelay = orig })
	var hits int32
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := accessServer(t, 100, &hits).GetLeaseAccess(ctx, "L1", ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context error while waiting to retry", err)
	}
}

func TestGetLeaseAccessDoesNotRetryOtherErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		code   string
		msg    string
	}{
		{409, `{"status":"fail","message":"Lease is not active","code":"LEASE_NOT_ACTIVE_FOR_ACCESS"}`, CodeLeaseNotActiveForAccess, "Lease is not active"},
		{403, `{"status":"fail","message":"Forbidden","code":"FORBIDDEN"}`, "FORBIDDEN", "Forbidden"},
		{503, `{"status":"error","message":"Service unavailable"}`, "", "Service unavailable"},
	} {
		var hits int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		_, err := New(srv.URL, "t").GetLeaseAccess(context.Background(), "L1", "")
		srv.Close()
		var ae *APIError
		if !errors.As(err, &ae) || ae.StatusCode != tc.status || ae.Code != tc.code || ae.Message != tc.msg {
			t.Errorf("%d: err = %#v", tc.status, err)
		}
		if hits != 1 {
			t.Errorf("%d: hits = %d, want no retry", tc.status, hits)
		}
	}
}

func TestExtractError(t *testing.T) {
	cases := []struct{ body, msg, code, ref string }{
		{`{"status":"fail","message":"m","code":"C","data":null}`, "m", "C", ""},
		{`{"status":"fail","data":{"message":"nested","code":"N"}}`, "nested", "N", ""},
		{`{"error":"Invalid token","reference":"r-1"}`, "Invalid token", "", "r-1"},
		{`{"error":"invalid_client","error_description":"bad secret"}`, "bad secret", "", ""},
		{`not json`, "", "", ""},
	}
	for _, c := range cases {
		msg, code, ref := extractError([]byte(c.body))
		if msg != c.msg || code != c.code || ref != c.ref {
			t.Errorf("extractError(%s) = %q,%q,%q; want %q,%q,%q", c.body, msg, code, ref, c.msg, c.code, c.ref)
		}
	}
}

func TestWellKnownOAuthConfig(t *testing.T) {
	c, got := server(t, 200, `{"status":"success","data":{"issuer":"https://abc.execute-api.example.com","token_endpoint":"https://abc/oauth/token",`+
		`"grant_types_supported":["client_credentials"],"sandbox_studio_cli":{"login_path":"/login","loopback_ports":[52900,52901,52902,52903,52904],"api_session_exchange":true}}}`)
	wk, err := c.WellKnownOAuthConfig(context.Background())
	if err != nil {
		t.Fatalf("WellKnownOAuthConfig: %v", err)
	}
	if got.method != "GET" || got.path != "/api/oauth/.well-known/oauth-authorization-server" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	cli := wk.SandboxStudioCLI
	if cli == nil || cli.LoginPath != "/login" || len(cli.LoopbackPorts) != 5 || cli.LoopbackPorts[4] != 52904 || !cli.APISessionExchange {
		t.Errorf("sandbox_studio_cli = %+v", cli)
	}

	c, _ = server(t, 200, `{"status":"success","data":{"issuer":"x","token_endpoint":"y","grant_types_supported":["client_credentials"]}}`)
	wk, err = c.WellKnownOAuthConfig(context.Background())
	if err != nil || wk.SandboxStudioCLI != nil {
		t.Errorf("IDC mode: block = %+v, err = %v; want absent", wk.SandboxStudioCLI, err)
	}
}
