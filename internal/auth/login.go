// Package auth — `sbs login`: the Sandbox Studio branded browser login.
//
// Flow (contract with the cli-enabler-v2 backend and UI):
//
//  1. Discovery: GET {base}/api/oauth/.well-known/oauth-authorization-server and
//     read the sandbox_studio_cli block (login_path, loopback_ports,
//     api_session_exchange). The block is absent in IDC mode.
//  2. Generate a per-login ephemeral P-256 key pair (the private half stays in
//     this process) and a random state, bind the first free loopback port on
//     127.0.0.1 and open
//     {base}{login_path}?cli_callback=http://127.0.0.1:<port>/callback&cli_state=<state>&cli_key=<pub>
//  3. The page (native email/password/MFA, or IdP sign-in) redirects the browser
//     to http://127.0.0.1:<port>/callback#enc=<ciphertext>&state=<state>. The
//     id_token is ENCRYPTED to cli_key (see logincrypto.go) and travels in the URL
//     fragment, which browsers never send to a server.
//  4. GET /callback therefore serves a tiny HTML+JS page that reads location.hash,
//     scrubs it from the address bar/history, and POSTs it to /callback/fragment.
//  5. The server checks Host/Origin, validates the state, and decrypts the
//     id_token with the private key.
//  6. The id_token is exchanged at POST {base}/api/auth/token-exchange (with
//     "client": "cli") for an API session, which is the only thing stored. The
//     id_token is discarded.
//
// Why encrypt: the loopback ports are a small fixed set, so another local process
// can bind one before us or race the browser to it. Without the private key it can
// observe only ciphertext.
//
// The CLI never talks to Cognito or AWS STS directly.
//
// All diagnostic output goes to stderr so stdout stays clean for -o json.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/sandboxstudio/sbs-cli/internal/api"
)

// callbackPath is the loopback redirect path the login page sends the browser to.
const callbackPath = "/callback"

// fragmentPath is the second loopback path the browser JS re-posts the captured
// URL fragment to, so the Go server can read it.
const fragmentPath = "/callback/fragment"

// browserOpener opens a URL in the user's browser. It is a package var so tests
// can capture the login URL instead of launching a browser.
var browserOpener = openURL

// ErrIDCMode means the deployment publishes no sandbox_studio_cli block, i.e. it
// runs in IAM Identity Center mode where CLI login is not available.
var ErrIDCMode = errors.New("CLI login requires a Cognito-mode deployment " +
	"(this Sandbox Studio deployment does not advertise CLI login)")

// Discover fetches the deployment's public discovery document and returns its
// validated sandbox_studio_cli block.
func Discover(ctx context.Context, baseURL string) (*api.SandboxStudioCLIConfig, error) {
	wk, err := api.New(baseURL, "").WellKnownOAuthConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the deployment's login settings: %w", err)
	}
	return validateCLIConfig(wk.SandboxStudioCLI)
}

// validateCLIConfig checks the discovery block has everything the login needs.
func validateCLIConfig(c *api.SandboxStudioCLIConfig) (*api.SandboxStudioCLIConfig, error) {
	if c == nil {
		return nil, ErrIDCMode
	}
	if !c.APISessionExchange {
		return nil, errors.New("this Sandbox Studio deployment does not support CLI API sessions " +
			"(api_session_exchange is false)")
	}
	if strings.TrimSpace(c.LoginPath) == "" {
		return nil, errors.New("the deployment's discovery document has no login_path")
	}
	if len(c.LoopbackPorts) == 0 {
		return nil, errors.New("the deployment's discovery document has no loopback_ports")
	}
	return c, nil
}

// Login runs the full `sbs login` flow: discovery, the branded browser login, and
// the exchange of the id_token for an API session, which is stored in the keyring.
func (a *Authenticator) Login(ctx context.Context) (Session, error) {
	if a.url == "" {
		return Session{}, errors.New("no deployment URL configured: run `sbs configure`")
	}
	cli, err := Discover(ctx, a.url)
	if err != nil {
		return Session{}, err
	}
	idToken, err := BrowserLogin(ctx, a.url, *cli)
	if err != nil {
		return Session{}, err
	}
	cs, err := api.New(a.url, "").ExchangeCLIIDToken(ctx, idToken)
	if err != nil {
		return Session{}, exchangeError(err)
	}
	return a.StoreSession(cs)
}

// exchangeError maps a token-exchange failure to a user-facing error that
// carries the server's message.
func exchangeError(err error) error {
	var ae *api.APIError
	if !errors.As(err, &ae) {
		return fmt.Errorf("exchanging the login for an API session: %w", err)
	}
	msg := ae.Message
	if msg == "" {
		msg = http.StatusText(ae.StatusCode)
	}
	if ae.Reference != "" {
		msg += " (reference: " + ae.Reference + ")"
	}
	return fmt.Errorf("login rejected by Sandbox Studio (%d): %s", ae.StatusCode, msg)
}

// randomState returns a cryptographically random state matching
// ^[A-Za-z0-9_-]{16,128}$ (32 base64url characters).
func randomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// BrowserLogin runs the branded browser login against baseURL using the
// discovered CLI settings and returns the decrypted Cognito id_token. The caller
// must exchange it for an API session and must not persist it.
func BrowserLogin(ctx context.Context, baseURL string, cli api.SandboxStudioCLIConfig) (string, error) {
	state, err := randomState()
	if err != nil {
		return "", err
	}
	// Per-login key pair: the login page encrypts the id_token to the public half.
	key, err := newLoginKey()
	if err != nil {
		return "", err
	}

	ln, port, err := listenLoopback(cli.LoopbackPorts)
	if err != nil {
		return "", err
	}
	defer ln.Close()

	resultCh := make(chan callbackResult, 1)
	srv := &http.Server{
		Handler:           fragmentCallbackHandler(state, key, port, resultCh),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	loginURL := buildLoginURL(baseURL, cli.LoginPath, callbackURL(port), state, key.publicParam())
	fmt.Fprintln(os.Stderr, "Opening your browser to log in…")
	fmt.Fprintln(os.Stderr, "If it does not open, visit:")
	fmt.Fprintln(os.Stderr, "  "+loginURL)
	if err := browserOpener(loginURL); err != nil {
		fmt.Fprintln(os.Stderr, "(could not auto-open a browser)")
	}

	select {
	case res := <-resultCh:
		return res.idToken, res.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// callbackURL is the loopback redirect target for a bound port. Always
// 127.0.0.1: "localhost" is resolver-dependent and is not what the page accepts.
func callbackURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", port, callbackPath)
}

// buildLoginURL builds {base}{login_path}?cli_callback=...&cli_state=...&cli_key=...
func buildLoginURL(baseURL, loginPath, callback, state, cliKey string) string {
	if !strings.HasPrefix(loginPath, "/") {
		loginPath = "/" + loginPath
	}
	q := url.Values{}
	q.Set("cli_callback", callback)
	q.Set("cli_state", state)
	q.Set("cli_key", cliKey)
	return BaseURL(baseURL) + loginPath + "?" + q.Encode()
}

// listenLoopback binds the first free port from ports on 127.0.0.1 and returns
// the listener and the chosen port.
func listenLoopback(ports []int) (net.Listener, int, error) {
	var lastErr error
	for _, port := range ports {
		ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			return ln, port, nil
		}
		lastErr = err
	}
	return nil, 0, fmt.Errorf("could not bind any loopback callback port %v: %w", ports, lastErr)
}

// callbackResult carries the decrypted id_token or an error from the loopback
// handler.
type callbackResult struct {
	idToken string
	err     error
}

// writeBrowserPage writes the branded, centered success/failure page.
func writeBrowserPage(w http.ResponseWriter, msg string) {
	writeBrandedPage(w, msg)
}

// openURL opens rawURL in the platform browser.
func openURL(rawURL string) error {
	if _, err := url.ParseRequestURI(rawURL); err != nil {
		return err
	}
	return browserCommand(rawURL).Start()
}

// captureScript runs on the loopback /callback page. It moves the URL fragment
// (which carries the encrypted token) out of the address bar and history, then
// POSTs it to the server and shows the server's response page.
const captureScript = `(function () {
  var hash = window.location.hash || "";
  if (hash.charAt(0) === "#") { hash = hash.substring(1); }
  history.replaceState(null, "", window.location.pathname);
  fetch("` + fragmentPath + `", {
    method: "POST",
    credentials: "omit",
    referrerPolicy: "no-referrer",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body: hash
  }).then(function (r) { return r.text(); }).then(function (html) {
    document.open(); document.write(html); document.close();
  }).catch(function () {
    document.body.textContent = "Login failed. You can close this window and retry.";
  });
})();`

// loopbackCSP locks the loopback pages down: no network except same-origin, no
// framing, and only the one inline capture script (pinned by hash).
func loopbackCSP() string {
	sum := sha256.Sum256([]byte(captureScript))
	return "default-src 'none'; img-src 'self' data:; style-src 'unsafe-inline'; " +
		"script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'; " +
		"connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
}

func setLoopbackHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", loopbackCSP())
}

// rejectPage writes a 400 branded page. Content-Type must be set before the
// status line is sent, so it is set here rather than left to writeBrandedPage.
func rejectPage(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	writeBrowserPage(w, msg)
}

// maxFragmentBody bounds the POSTed fragment (an encrypted JWT is a few KB).
const maxFragmentBody = 16 * 1024

// fragmentCallbackHandler serves the two-step loopback flow:
//
//   - GET /callback: returns an HTML+JS page that reads location.hash and POSTs it
//     to /callback/fragment.
//   - POST /callback/fragment: validates state, decrypts `enc` with the login key,
//     and reports the outcome on resultCh exactly once.
//
// Hardening (any local process or web page can reach a loopback port):
//   - The Host header must be the loopback address and port we bound (blocks DNS
//     rebinding); a present Origin must match it (blocks cross-site POSTs).
//   - A request with the wrong state is rejected WITHOUT ending the login, so a
//     stray request cannot abort or poison a login in progress.
//   - Only a request carrying the correct state can produce a result.
func fragmentCallbackHandler(wantState string, key *loginKey, port int, resultCh chan<- callbackResult) http.Handler {
	// The browser is always sent to 127.0.0.1; "localhost" is resolver-dependent.
	allowedHosts := map[string]bool{
		fmt.Sprintf("127.0.0.1:%d", port): true,
	}
	deliver := func(res callbackResult) {
		select {
		case resultCh <- res:
		default: // a result was already delivered; ignore duplicates
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc(logoPath, serveLogo)

	// Step 1: the browser lands here with the encrypted token in the URL fragment.
	// The fragment is not sent to the server, so serve JS that forwards it.
	mux.HandleFunc(callbackPath, func(w http.ResponseWriter, r *http.Request) {
		// Guard against the fragment subpath falling through to this handler.
		if r.URL.Path != callbackPath || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		writeFragmentCapturePage(w)
	})

	// Step 2: the JS POSTs the parsed fragment here.
	mux.HandleFunc(fragmentPath, func(w http.ResponseWriter, r *http.Request) {
		setLoopbackHeaders(w)
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxFragmentBody)
		if err := r.ParseForm(); err != nil {
			rejectPage(w, "Login failed (bad request). You can close this window.")
			return
		}
		q := r.PostForm

		// Ignore anything that does not carry our state; it cannot end the login.
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(wantState)) != 1 {
			rejectPage(w, "Login failed (state mismatch). You can close this window.")
			return
		}

		if e := q.Get("error"); e != "" {
			desc := q.Get("error_description")
			if desc == "" {
				desc = e
			}
			writeBrowserPage(w, "Login failed. You can close this window.")
			deliver(callbackResult{err: fmt.Errorf("login error: %s", desc)})
			return
		}

		enc := q.Get("enc")
		if enc == "" {
			rejectPage(w, "Login failed (no token). You can close this window.")
			deliver(callbackResult{err: fmt.Errorf("no encrypted token in login callback")})
			return
		}

		idToken, err := key.decrypt(wantState, enc)
		if err != nil {
			rejectPage(w, "Login failed (could not verify token). You can close this window.")
			deliver(callbackResult{err: fmt.Errorf("login token rejected: %w", err)})
			return
		}

		writeBrowserPage(w, "Login successful. You can close this window and return to the terminal.")
		deliver(callbackResult{idToken: idToken})
	})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allowedHosts[r.Host] {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// writeFragmentCapturePage serves the tiny HTML+JS page that reads the URL
// fragment (where the SBS page delivered the encrypted token) and POSTs it to the
// fragment subpath so the Go server can read it.
func writeFragmentCapturePage(w http.ResponseWriter) {
	setLoopbackHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Signing you in…</title>
<style>
  html, body { height: 100%; margin: 0; }
  body {
    display: flex; align-items: center; justify-content: center;
    background: #ffffff; color: #1a1f3d;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
  }
  .card { text-align: center; padding: 2.5rem 3rem; }
  .card img { width: 260px; max-width: 70vw; height: auto; margin-bottom: 1.75rem; }
  .card p { font-size: 1.1rem; margin: 0; }
</style>
</head>
<body>
  <div class="card">
    <img src="` + logoDataURI + `" alt="Sandbox Studio">
    <p>Completing sign-in… you can close this window shortly.</p>
  </div>
<script>` + captureScript + `</script>
<noscript>JavaScript is required to complete sign-in. Please enable it and retry.</noscript>
</body>
</html>`
	_, _ = io.WriteString(w, page)
}
