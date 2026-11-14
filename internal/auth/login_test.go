package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sandboxstudio/sbs-cli/internal/api"
	"github.com/sandboxstudio/sbs-cli/internal/keyring"
)

const testState = "state-0123456789abcdef"

// TestBuildLoginURL asserts the login URL is {base}{login_path} with the
// cli_callback/cli_state/cli_key params, and that a trailing "/api" or slash on
// the configured URL is stripped.
func TestBuildLoginURL(t *testing.T) {
	cases := []struct {
		name, url, path, wantBase string
	}{
		{"plain", "https://trial.example.com", "/login", "https://trial.example.com/login"},
		{"trailing slash", "https://trial.example.com/", "/login", "https://trial.example.com/login"},
		{"api suffix", "https://trial.example.com/api", "/login", "https://trial.example.com/login"},
		{"api suffix slash", "https://trial.example.com/api/", "/login", "https://trial.example.com/login"},
		{"path without slash", "https://trial.example.com", "signin", "https://trial.example.com/signin"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := buildLoginURL(c.url, c.path, "http://127.0.0.1:52900/callback", "st-1234567890abcdef", "PUBKEY")
			u, err := url.Parse(got)
			if err != nil {
				t.Fatalf("not a URL: %v", err)
			}
			if base := u.Scheme + "://" + u.Host + u.Path; base != c.wantBase {
				t.Errorf("base = %q, want %q", base, c.wantBase)
			}
			q := u.Query()
			if len(q) != 3 {
				t.Errorf("want exactly cli_callback, cli_state, cli_key; got %v", q)
			}
			if q.Get("cli_callback") != "http://127.0.0.1:52900/callback" {
				t.Errorf("cli_callback = %q", q.Get("cli_callback"))
			}
			if q.Get("cli_state") != "st-1234567890abcdef" {
				t.Errorf("cli_state = %q", q.Get("cli_state"))
			}
			if q.Get("cli_key") != "PUBKEY" {
				t.Errorf("cli_key = %q", q.Get("cli_key"))
			}
		})
	}
}

func TestCallbackURLUses127001(t *testing.T) {
	if got := callbackURL(52901); got != "http://127.0.0.1:52901/callback" {
		t.Errorf("callbackURL = %q", got)
	}
}

func TestRandomStateFormat(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		s, err := randomState()
		if err != nil {
			t.Fatal(err)
		}
		if !loginStatePattern.MatchString(s) {
			t.Fatalf("state %q does not match %s", s, loginStatePattern)
		}
		if seen[s] {
			t.Fatalf("state repeated: %q", s)
		}
		seen[s] = true
	}
}

// freePorts returns n distinct ports that were free a moment ago.
func freePorts(t *testing.T, n int) []int {
	t.Helper()
	var ports []int
	var lns []net.Listener
	for i := 0; i < n; i++ {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		lns = append(lns, ln)
		ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
	}
	for _, ln := range lns {
		ln.Close()
	}
	return ports
}

// The first free port from loopback_ports is bound, on 127.0.0.1.
func TestListenLoopbackPicksFirstFreePort(t *testing.T) {
	ports := freePorts(t, 2)
	busy, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", ports[0]))
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	ln, port, err := listenLoopback(ports)
	if err != nil {
		t.Fatalf("listenLoopback: %v", err)
	}
	defer ln.Close()
	if port != ports[1] {
		t.Errorf("port = %d, want %d (first is busy)", port, ports[1])
	}
	if host := ln.Addr().(*net.TCPAddr).IP.String(); host != "127.0.0.1" {
		t.Errorf("bound on %s, want 127.0.0.1", host)
	}

	if _, _, err := listenLoopback([]int{ports[0]}); err == nil {
		t.Error("expected an error when every port is busy")
	}
}

func TestValidateCLIConfig(t *testing.T) {
	good := &api.SandboxStudioCLIConfig{LoginPath: "/login", LoopbackPorts: []int{52900}, APISessionExchange: true}
	if _, err := validateCLIConfig(good); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
	if _, err := validateCLIConfig(nil); !errors.Is(err, ErrIDCMode) ||
		!strings.Contains(err.Error(), "CLI login requires a Cognito-mode deployment") {
		t.Errorf("IDC mode: err = %v", err)
	}
	for name, c := range map[string]*api.SandboxStudioCLIConfig{
		"no exchange": {LoginPath: "/login", LoopbackPorts: []int{1}},
		"no path":     {LoopbackPorts: []int{1}, APISessionExchange: true},
		"no ports":    {LoginPath: "/login", APISessionExchange: true},
	} {
		if _, err := validateCLIConfig(c); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDiscoverParsesCLIBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/oauth/.well-known/oauth-authorization-server" || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected request %s (auth %q)", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"issuer":"https://raw.execute-api.example.com",` +
			`"token_endpoint":"https://raw.execute-api.example.com/oauth/token","grant_types_supported":["client_credentials"],` +
			`"sandbox_studio_cli":{"login_path":"/login","loopback_ports":[52900,52901,52902,52903,52904],"api_session_exchange":true}}}`))
	}))
	defer srv.Close()
	c, err := Discover(context.Background(), srv.URL+"/")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if c.LoginPath != "/login" || len(c.LoopbackPorts) != 5 || c.LoopbackPorts[0] != 52900 || !c.APISessionExchange {
		t.Errorf("parsed = %+v", c)
	}
}

func TestDiscoverIDCMode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"issuer":"x","token_endpoint":"y","grant_types_supported":["client_credentials"]}}`))
	}))
	defer srv.Close()
	if _, err := Discover(context.Background(), srv.URL); !errors.Is(err, ErrIDCMode) {
		t.Errorf("err = %v, want ErrIDCMode", err)
	}
}

// newHandlerServer starts the loopback handler on a test server and returns it
// with the result channel and the login key whose public half a login page would
// encrypt to.
func newHandlerServer(t *testing.T) (*httptest.Server, chan callbackResult, *loginKey) {
	t.Helper()
	key, err := newLoginKey()
	if err != nil {
		t.Fatal(err)
	}
	resultCh := make(chan callbackResult, 1)
	srv := httptest.NewUnstartedServer(nil)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	srv.Config.Handler = fragmentCallbackHandler(testState, key, port, resultCh)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv, resultCh, key
}

func postFragment(t *testing.T, srv *httptest.Server, form url.Values, mutate func(*http.Request)) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+fragmentPath, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if mutate != nil {
		mutate(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST fragment: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func expectNoResult(t *testing.T, ch chan callbackResult) {
	t.Helper()
	select {
	case res := <-ch:
		t.Fatalf("unexpected result delivered: %+v", res)
	case <-time.After(150 * time.Millisecond):
	}
}

func expectResult(t *testing.T, ch chan callbackResult) callbackResult {
	t.Helper()
	select {
	case res := <-ch:
		return res
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not report a result")
		return callbackResult{}
	}
}

// TestCapturePageForwardsFragmentByPOSTWithLockedDownHeaders: GET /callback serves
// the capture page. It must POST (not navigate to a query string, which would put
// the payload in browser history), scrub the address bar, and carry a CSP pinned to
// the one inline script.
func TestCapturePageForwardsFragmentByPOSTWithLockedDownHeaders(t *testing.T) {
	srv, _, _ := newHandlerServer(t)

	resp, err := http.Get(srv.URL + callbackPath)
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	page := string(body)
	for _, want := range []string{fragmentPath, `method: "POST"`, "history.replaceState"} {
		if !strings.Contains(page, want) {
			t.Errorf("capture page missing %q", want)
		}
	}
	if strings.Contains(page, "location.replace") {
		t.Error("capture page must not navigate with the payload in the URL (browser history)")
	}
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "script-src 'sha256-") {
		t.Errorf("CSP must pin the inline script by hash, got %q", resp.Header.Get("Content-Security-Policy"))
	}
	if resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Error("missing Cache-Control/Referrer-Policy headers")
	}
}

func TestPOSTWithValidStateAndCiphertextDeliversDecryptedToken(t *testing.T) {
	srv, resultCh, key := newHandlerServer(t)
	enc := encryptForTest(t, key.publicParam(), testState, "jwt-abc")

	resp := postFragment(t, srv, url.Values{"enc": {enc}, "state": {testState}}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	res := expectResult(t, resultCh)
	if res.err != nil {
		t.Fatalf("unexpected error: %v", res.err)
	}
	if res.idToken != "jwt-abc" {
		t.Errorf("captured id_token = %q, want jwt-abc", res.idToken)
	}
}

// A process that squats the port sees only ciphertext it cannot open; if it
// replays it (or substitutes its own), the login must fail rather than yield a token.
func TestPOSTEncryptedToAnotherKeyIsRejected(t *testing.T) {
	srv, resultCh, _ := newHandlerServer(t)
	attacker, _ := newLoginKey()
	enc := encryptForTest(t, attacker.publicParam(), testState, "forged")

	resp := postFragment(t, srv, url.Values{"enc": {enc}, "state": {testState}}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	res := expectResult(t, resultCh)
	if res.err == nil || res.idToken != "" {
		t.Errorf("expected a rejection with no token, got %+v", res)
	}
}

// A plaintext id_token (e.g. from an old/misbehaving server) must never be accepted.
func TestPOSTRejectsPlaintextIDToken(t *testing.T) {
	srv, resultCh, _ := newHandlerServer(t)
	resp := postFragment(t, srv, url.Values{"id_token": {"a.b.c"}, "state": {testState}}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	res := expectResult(t, resultCh)
	if res.err == nil || res.idToken != "" {
		t.Errorf("a plaintext id_token must not be accepted, got %+v", res)
	}
}

// A stray request with the wrong state must NOT end (or poison) the login in
// progress — otherwise any web page could abort a login.
func TestWrongStateIsIgnoredAndDoesNotAbortLogin(t *testing.T) {
	srv, resultCh, key := newHandlerServer(t)

	resp := postFragment(t, srv, url.Values{"enc": {"x"}, "state": {"WRONG-STATE-0123456789"}}, nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	expectNoResult(t, resultCh)

	// The genuine login still completes afterwards.
	enc := encryptForTest(t, key.publicParam(), testState, "jwt-real")
	postFragment(t, srv, url.Values{"enc": {enc}, "state": {testState}}, nil)
	if res := expectResult(t, resultCh); res.err != nil || res.idToken != "jwt-real" {
		t.Errorf("genuine login after a stray request = %+v", res)
	}
}

func TestLoginErrorWithCorrectStateIsReported(t *testing.T) {
	srv, resultCh, _ := newHandlerServer(t)
	postFragment(t, srv, url.Values{"error": {"access_denied"}, "state": {testState}}, nil)
	res := expectResult(t, resultCh)
	if res.err == nil || !strings.Contains(res.err.Error(), "access_denied") {
		t.Errorf("expected access_denied error, got %+v", res)
	}
}

func TestMissingCiphertextIsAnError(t *testing.T) {
	srv, resultCh, _ := newHandlerServer(t)
	postFragment(t, srv, url.Values{"state": {testState}}, nil)
	if res := expectResult(t, resultCh); res.err == nil {
		t.Error("expected a missing-token error")
	}
}

// Loopback servers are reachable by any local process and web page.
func TestRejectsForeignHostOriginAndMethods(t *testing.T) {
	srv, resultCh, key := newHandlerServer(t)
	enc := encryptForTest(t, key.publicParam(), testState, "jwt")
	form := url.Values{"enc": {enc}, "state": {testState}}

	t.Run("DNS rebinding: foreign Host", func(t *testing.T) {
		resp := postFragment(t, srv, form, func(r *http.Request) { r.Host = "evil.example.com" })
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
	})
	t.Run("localhost is not accepted (resolver-dependent)", func(t *testing.T) {
		resp := postFragment(t, srv, form, func(r *http.Request) {
			_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
			r.Host = "localhost:" + port
		})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
	})
	t.Run("cross-site POST: foreign Origin", func(t *testing.T) {
		resp := postFragment(t, srv, form, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example.com") })
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("status = %d, want 403", resp.StatusCode)
		}
	})
	t.Run("GET on the fragment path", func(t *testing.T) {
		resp, err := http.Get(srv.URL + fragmentPath + "?enc=" + url.QueryEscape(enc) + "&state=" + testState)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("status = %d, want 405", resp.StatusCode)
		}
	})
	t.Run("oversize body", func(t *testing.T) {
		big := url.Values{"enc": {strings.Repeat("A", 64*1024)}, "state": {testState}}
		resp := postFragment(t, srv, big, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", resp.StatusCode)
		}
	})
	// None of the above may have produced a result.
	expectNoResult(t, resultCh)
}

// loginServer fakes the backend's discovery document and CLI token exchange.
type loginServer struct {
	*httptest.Server
	ports        []int
	exchangeCode int    // status for the exchange; 200 when zero
	exchangeBody string // reply body for a non-200 exchange
	gotIDToken   string // idToken posted to the exchange
	session      string
}

func newLoginServer(t *testing.T) *loginServer {
	t.Helper()
	ls := &loginServer{ports: freePorts(t, 2), session: makeJWT(8 * time.Hour)}
	ls.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/oauth/.well-known/oauth-authorization-server":
			fmt.Fprintf(w, `{"status":"success","data":{"issuer":"https://raw.example.com","token_endpoint":"x",`+
				`"grant_types_supported":["client_credentials"],"sandbox_studio_cli":{"login_path":"/login",`+
				`"loopback_ports":[%d,%d],"api_session_exchange":true}}}`, ls.ports[0], ls.ports[1])
		case "/api/auth/token-exchange":
			if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("exchange request %s %q", r.Method, r.Header.Get("Content-Type"))
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			ls.gotIDToken = body["idToken"]
			if body["client"] != "cli" {
				t.Errorf(`exchange body client = %q, want "cli"`, body["client"])
			}
			if ls.exchangeCode != 0 {
				w.WriteHeader(ls.exchangeCode)
				_, _ = w.Write([]byte(ls.exchangeBody))
				return
			}
			fmt.Fprintf(w, `{"token":%q,"user":{"userId":"u1"},"expiresAt":"2030-01-01T08:00:00.000Z"}`, ls.session)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(ls.Close)
	return ls
}

// stubLoginPage replaces the browser with one that behaves like the login page:
// it encrypts idToken to cli_key and sends the fragment to the loopback, the way
// the capture page's script does. enc, if set, overrides the payload.
func stubLoginPage(t *testing.T, idToken string, encFor func(cliKey, state string) string) *string {
	t.Helper()
	var seen string
	orig := browserOpener
	browserOpener = func(loginURL string) error {
		seen = loginURL
		u, err := url.Parse(loginURL)
		if err != nil {
			return err
		}
		q := u.Query()
		cb, state, cliKey := q.Get("cli_callback"), q.Get("cli_state"), q.Get("cli_key")
		enc := encryptForTest(t, cliKey, state, idToken)
		if encFor != nil {
			enc = encFor(cliKey, state)
		}
		go func() {
			resp, err := http.PostForm(cb+"/fragment", url.Values{"enc": {enc}, "state": {state}})
			if err == nil {
				resp.Body.Close()
			}
		}()
		return nil
	}
	t.Cleanup(func() { browserOpener = orig })
	return &seen
}

// TestLoginFullFlowStoresOnlyTheSession runs `sbs login` end to end: discovery,
// the branded page (stubbed), fragment decryption, and the session exchange.
func TestLoginFullFlowStoresOnlyTheSession(t *testing.T) {
	ls := newLoginServer(t)
	idToken := makeJWT(time.Hour)
	seen := stubLoginPage(t, idToken, nil)

	kr := keyring.NewMemory()
	_ = kr.Set("oidc-id-token:"+ls.URL, "stale-cognito-token") // from an older CLI
	a := New(kr, ls.URL+"/api")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sess, err := a.Login(ctx)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}

	u, _ := url.Parse(*seen)
	q := u.Query()
	if u.Scheme+"://"+u.Host+u.Path != ls.URL+"/login" {
		t.Errorf("login page = %q, want %s/login", *seen, ls.URL)
	}
	if q.Get("cli_callback") != fmt.Sprintf("http://127.0.0.1:%d/callback", ls.ports[0]) {
		t.Errorf("cli_callback = %q", q.Get("cli_callback"))
	}
	if !loginStatePattern.MatchString(q.Get("cli_state")) {
		t.Errorf("cli_state = %q", q.Get("cli_state"))
	}
	if k := q.Get("cli_key"); len(k) != 87 || strings.Contains(k, "=") {
		t.Errorf("cli_key = %q (len %d), want 87 base64url chars", k, len(k))
	}

	if ls.gotIDToken != idToken {
		t.Errorf("exchanged idToken = %q, want the decrypted one", ls.gotIDToken)
	}
	want := Session{Token: ls.session, ExpiresAt: time.Date(2030, 1, 1, 8, 0, 0, 0, time.UTC), URL: ls.URL}
	if sess != want {
		t.Errorf("session = %+v, want %+v", sess, want)
	}

	// The keyring holds exactly the session (token, expiresAt, url) — no id_token.
	raw, err := kr.Get(keySessionPrefix + ls.URL)
	if err != nil {
		t.Fatalf("session not stored: %v", err)
	}
	var stored map[string]any
	_ = json.Unmarshal([]byte(raw), &stored)
	if len(stored) != 3 || stored["token"] != ls.session || stored["url"] != ls.URL || stored["expiresAt"] != "2030-01-01T08:00:00Z" {
		t.Errorf("stored session = %s", raw)
	}
	if strings.Contains(raw, idToken) {
		t.Error("the Cognito id_token must not be stored")
	}
	if _, err := kr.Get("oidc-id-token:" + ls.URL); err == nil {
		t.Error("legacy Cognito tokens should be removed on login")
	}
	if tok, err := a.Token(ctx); err != nil || tok != ls.session {
		t.Errorf("Token() = %q, %v; want the session token", tok, err)
	}
}

func TestLoginExchangeErrorIsSurfaced(t *testing.T) {
	ls := newLoginServer(t)
	ls.exchangeCode = 403
	ls.exchangeBody = `{"error":"User is not allowed to use the CLI","reference":"ref-123"}`
	stubLoginPage(t, makeJWT(time.Hour), nil)

	kr := keyring.NewMemory()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := New(kr, ls.URL).Login(ctx)
	if err == nil || !strings.Contains(err.Error(), "User is not allowed to use the CLI") || !strings.Contains(err.Error(), "ref-123") {
		t.Fatalf("err = %v, want the server message and reference", err)
	}
	if _, err := kr.Get(keySessionPrefix + ls.URL); err == nil {
		t.Error("nothing may be stored when the exchange fails")
	}
}

// A different local process answering on the callback with its own key must not
// be able to complete the login.
func TestLoginRejectsTokenEncryptedToSomeoneElse(t *testing.T) {
	ls := newLoginServer(t)
	attacker, _ := newLoginKey()
	stubLoginPage(t, "", func(_, state string) string {
		return encryptForTest(t, attacker.publicParam(), state, makeJWT(time.Hour))
	})

	kr := keyring.NewMemory()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := New(kr, ls.URL).Login(ctx); err == nil {
		t.Fatal("login must fail when the token was not encrypted to this process's key")
	}
	if ls.gotIDToken != "" {
		t.Error("no exchange may happen on a rejected token")
	}
	if _, err := kr.Get(keySessionPrefix + ls.URL); err == nil {
		t.Error("nothing may be stored on a rejected token")
	}
}

// TestFragmentCaptureServesLogo ensures the embedded logo is served on the branded
// pages' logo path (so the centered success page can render it).
func TestFragmentCaptureServesLogo(t *testing.T) {
	srv, _, _ := newHandlerServer(t)

	resp, err := http.Get(srv.URL + logoPath)
	if err != nil {
		t.Fatalf("GET logo: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logo status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("logo Content-Type = %q, want image/png", ct)
	}
	b, _ := io.ReadAll(resp.Body)
	if len(b) == 0 {
		t.Error("logo body is empty")
	}
}

// TestBrandedPageInlinesLogo asserts the success page embeds the logo as a
// self-contained data: URI (not an external src), so it renders even as the
// loopback server shuts down right after capturing the token. This is the
// regression guard for the broken-image bug.
func TestBrandedPageInlinesLogo(t *testing.T) {
	rec := httptest.NewRecorder()
	writeBrandedPage(rec, "Login successful. You can close this window and return to the terminal.")
	body := rec.Body.String()

	if !strings.Contains(body, "src=\"data:image/png;base64,") {
		t.Error("branded page must inline the logo as a data: URI")
	}
	if strings.Contains(body, "src=\"/logo.png\"") {
		t.Error("branded page must not reference the logo via an external path (races server shutdown)")
	}
	if !strings.Contains(body, "Login successful.") {
		t.Error("branded page missing the success message")
	}
}
