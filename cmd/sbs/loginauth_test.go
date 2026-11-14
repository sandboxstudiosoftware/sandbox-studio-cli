package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/api"
	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/auth"
	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/config"
	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/keyring"
)

// testJWT builds an unsigned JWT with the given nested user and exp.
func testJWT(ttl time.Duration, role string) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`))
	p, _ := json.Marshal(map[string]any{
		"user": map[string]any{"userId": "u-login", "email": "me@example.com", "role": role},
		"via":  "cli",
		"exp":  time.Now().Add(ttl).Unix(),
	})
	return h + "." + base64.RawURLEncoding.EncodeToString(p) + ".sig"
}

// accessReply is one canned response for POST /api/leases/{id}/access.
type accessReply struct {
	status int
	body   string
}

func accessOK(exp time.Time) accessReply {
	return accessReply{200, `{"status":"success","data":{"type":"FEDERATION",` +
		`"consoleUrl":"https://signin.aws.amazon.com/federation?Action=login&SigninToken=abc",` +
		`"credentials":{"accessKeyId":"ASIASESSION","secretAccessKey":"sk","sessionToken":"st",` +
		`"expiration":"` + exp.UTC().Format(time.RFC3339) + `"}}}`}
}

// loginBackend fakes the cli-enabler-v2 backend for a user logged in with an API
// session and NO API client configured.
type loginBackend struct {
	run     func(args ...string) (string, string, error)
	mem     keyring.Keyring
	url     string
	session string

	mu          sync.Mutex
	leaseAuth   string        // Authorization header seen on GET /leases
	accessAuth  string        // Authorization header seen on POST /access
	accessRoles []string      // "role" bodies seen on POST /access
	accessQueue []accessReply // replies served in order; the last one repeats
	discovery   string        // body for the discovery document
}

func (lb *loginBackend) accessCalls() int {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	return len(lb.accessRoles)
}

func newLoginBackend(t *testing.T, loggedIn bool) *loginBackend {
	t.Helper()
	lb := &loginBackend{
		session:     testJWT(time.Hour, "Admin"),
		accessQueue: []accessReply{accessOK(time.Now().Add(12 * time.Hour))},
		discovery: `{"status":"success","data":{"issuer":"https://abc.execute-api.example.com","token_endpoint":"x",` +
			`"grant_types_supported":["client_credentials"],"sandbox_studio_cli":{"login_path":"/login",` +
			`"loopback_ports":[52900,52901],"api_session_exchange":true}}}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lb.mu.Lock()
		defer lb.mu.Unlock()
		switch {
		case r.URL.Path == "/api/oauth/.well-known/oauth-authorization-server":
			_, _ = w.Write([]byte(lb.discovery))
		case r.URL.Path == "/api/leases" && r.Method == http.MethodGet:
			lb.leaseAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"leaseId":"L1","status":"Active","template":{"name":"Dev"},"owner":{"email":"me@example.com"}}],"nextPageIdentifier":null}}`))
		case r.URL.Path == "/api/leases/L1/access" && r.Method == http.MethodPost:
			lb.accessAuth = r.Header.Get("Authorization")
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("access Content-Type = %q", ct)
			}
			var body struct{ Role string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			lb.accessRoles = append(lb.accessRoles, body.Role)
			reply := lb.accessQueue[0]
			if len(lb.accessQueue) > 1 {
				lb.accessQueue = lb.accessQueue[1:]
			}
			w.WriteHeader(reply.status)
			_, _ = w.Write([]byte(reply.body))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	lb.url = srv.URL

	t.Setenv(config.EnvConfigDir, t.TempDir())
	t.Setenv(config.EnvURL, srv.URL)
	t.Setenv(config.EnvClientID, "")
	t.Setenv(config.EnvClientSecret, "")

	mem := keyring.NewMemory()
	if loggedIn {
		lb.storeSession(t, mem, time.Now().Add(time.Hour))
	}
	lb.mem = mem
	newKeyring = func() keyring.Keyring { return mem }
	interactiveTerminal = func() bool { return false }
	origDelay := api.AccessRetryDelay
	api.AccessRetryDelay = time.Millisecond
	t.Cleanup(func() {
		newKeyring = keyring.New
		interactiveTerminal = func() bool { return false }
		api.AccessRetryDelay = origDelay
	})

	lb.run = func(args ...string) (string, string, error) {
		flagContext, flagURL, flagOutput, flagDebug = "", "", "", false
		return captureStd(func() error {
			root := newRootCmd()
			root.SetArgs(args)
			return root.Execute()
		})
	}
	return lb
}

func (lb *loginBackend) storeSession(t *testing.T, kr keyring.Keyring, exp time.Time) {
	t.Helper()
	if _, err := auth.New(kr, lb.url).StoreSession(&api.CLISession{
		Token: lb.session, ExpiresAt: exp.UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCmdLeaseListUsesAPISession(t *testing.T) {
	lb := newLoginBackend(t, true)
	stdout, _, err := lb.run("lease", "list")
	if err != nil {
		t.Fatalf("lease list with a session: %v", err)
	}
	if !strings.Contains(stdout, "L1") {
		t.Errorf("missing lease row:\n%s", stdout)
	}
	if lb.leaseAuth != "Bearer "+lb.session {
		t.Errorf("API call should carry the stored session token, got %q", lb.leaseAuth)
	}
}

func TestCmdNotLoggedInSaysLogin(t *testing.T) {
	lb := newLoginBackend(t, false)
	_, _, err := lb.run("lease", "list")
	if err == nil || !strings.Contains(err.Error(), "sbs login") {
		t.Fatalf("err = %v, want a hint to run `sbs login`", err)
	}
}

// There is no refresh flow: an expired session requires `sbs login` again, even
// at an interactive terminal (no browser is opened implicitly).
func TestCmdExpiredSessionRequiresLogin(t *testing.T) {
	lb := newLoginBackend(t, false)
	lb.storeSession(t, lb.mem, time.Now().Add(-time.Minute))
	interactiveTerminal = func() bool { return true }

	for _, args := range [][]string{{"lease", "list"}, {"creds", "--lease", "L1"}} {
		stdout, _, err := lb.run(args...)
		if err == nil || !strings.Contains(err.Error(), "expired") || !strings.Contains(err.Error(), "sbs login") {
			t.Fatalf("%v: err = %v, want an expired-session hint", args, err)
		}
		if stdout != "" {
			t.Errorf("%v: stdout must stay empty on error, got %q", args, stdout)
		}
	}
	if lb.accessCalls() != 0 {
		t.Error("no API call should be made with an expired session")
	}
}

func TestCmdCredsUsesSessionAndCaches(t *testing.T) {
	lb := newLoginBackend(t, true)

	stdout, _, err := lb.run("creds", "--lease", "L1")
	if err != nil {
		t.Fatalf("creds: %v", err)
	}
	var pc map[string]any
	if err := json.Unmarshal([]byte(stdout), &pc); err != nil {
		t.Fatalf("stdout is not JSON: %v (%q)", err, stdout)
	}
	if pc["Version"] != float64(1) || pc["AccessKeyId"] != "ASIASESSION" || pc["SecretAccessKey"] != "sk" ||
		pc["SessionToken"] != "st" || pc["Expiration"] == "" {
		t.Errorf("credential_process output = %v", pc)
	}
	if lb.accessAuth != "Bearer "+lb.session {
		t.Errorf("Authorization = %q, want the session token", lb.accessAuth)
	}
	if lb.accessRoles[0] != "USER" {
		t.Errorf("role = %q, want USER", lb.accessRoles[0])
	}

	// The second call is served from the keyring cache.
	stdout2, _, err := lb.run("creds", "--lease", "L1")
	if err != nil {
		t.Fatalf("second creds: %v", err)
	}
	if stdout2 != stdout {
		t.Errorf("cached output differs:\n%s\n%s", stdout, stdout2)
	}
	if n := lb.accessCalls(); n != 1 {
		t.Errorf("access calls = %d, want 1 (cached)", n)
	}

	// ADMIN is cached separately and sent as role ADMIN.
	if _, _, err := lb.run("creds", "--lease", "L1", "--role", "admin"); err != nil {
		t.Fatalf("creds --role admin: %v", err)
	}
	if n := lb.accessCalls(); n != 2 || lb.accessRoles[1] != "ADMIN" {
		t.Errorf("access roles = %v, want a second call with ADMIN", lb.accessRoles)
	}
}

// Credentials within 5 minutes of expiring are not served from the cache.
func TestCmdCredsRefetchesNearExpiry(t *testing.T) {
	lb := newLoginBackend(t, true)
	lb.accessQueue = []accessReply{accessOK(time.Now().Add(4 * time.Minute)), accessOK(time.Now().Add(12 * time.Hour))}
	for i := 0; i < 2; i++ {
		if _, _, err := lb.run("creds", "--lease", "L1"); err != nil {
			t.Fatalf("creds #%d: %v", i, err)
		}
	}
	if n := lb.accessCalls(); n != 2 {
		t.Errorf("access calls = %d, want 2 (short-lived creds must not be cached)", n)
	}
}

func TestCmdCredsRetriesProvisioning(t *testing.T) {
	lb := newLoginBackend(t, true)
	prov := accessReply{503, `{"status":"error","code":"FEDERATED_ACCESS_PROVISIONING","message":"Access is being provisioned"}`}
	lb.accessQueue = []accessReply{prov, prov, accessOK(time.Now().Add(time.Hour))}
	stdout, _, err := lb.run("creds", "--lease", "L1")
	if err != nil {
		t.Fatalf("creds: %v", err)
	}
	if !strings.Contains(stdout, "ASIASESSION") || lb.accessCalls() != 3 {
		t.Errorf("calls = %d, stdout = %q", lb.accessCalls(), stdout)
	}
}

func TestCmdCredsProvisioningGivesUpAfterThreeRetries(t *testing.T) {
	lb := newLoginBackend(t, true)
	lb.accessQueue = []accessReply{{503, `{"status":"error","code":"FEDERATED_ACCESS_PROVISIONING","message":"Access is being provisioned"}`}}
	stdout, _, err := lb.run("creds", "--lease", "L1")
	if err == nil || !strings.Contains(err.Error(), "Access is being provisioned") {
		t.Fatalf("err = %v, want the provisioning message", err)
	}
	if n := lb.accessCalls(); n != 4 {
		t.Errorf("access calls = %d, want 4 (1 + 3 retries)", n)
	}
	if stdout != "" {
		t.Errorf("stdout must be empty on error, got %q", stdout)
	}
}

func TestCmdCredsAccessErrors(t *testing.T) {
	cases := []struct {
		name  string
		reply accessReply
		want  []string
	}{
		{"401 means log in again", accessReply{401, `{"status":"fail","message":"Session expired"}`},
			[]string{"Session expired", "sbs login"}},
		{"403 shows the message", accessReply{403, `{"status":"fail","message":"You do not have access to this lease"}`},
			[]string{"You do not have access to this lease"}},
		{"409 lease not active", accessReply{409, `{"status":"fail","code":"LEASE_NOT_ACTIVE_FOR_ACCESS","message":"Lease is Suspended"}`},
			[]string{"not active", "Lease is Suspended"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lb := newLoginBackend(t, true)
			lb.accessQueue = []accessReply{c.reply}
			_, _, err := lb.run("creds", "--lease", "L1")
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, w := range c.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("err = %q, missing %q", err, w)
				}
			}
			if c.reply.status == 403 && strings.Contains(err.Error(), "sbs login") {
				t.Errorf("a 403 with a valid session must not ask for a login: %v", err)
			}
		})
	}
}

func TestCmdCredsRejectsUnknownRole(t *testing.T) {
	lb := newLoginBackend(t, true)
	if _, _, err := lb.run("creds", "--lease", "L1", "--role", "OWNER"); err == nil || !strings.Contains(err.Error(), "--role") {
		t.Fatalf("err = %v, want an invalid role error", err)
	}
}

func TestCmdLoginConsoleOpensConsoleURL(t *testing.T) {
	lb := newLoginBackend(t, true)
	var opened []string
	orig := openBrowser
	openBrowser = func(u string) error { opened = append(opened, u); return nil }
	t.Cleanup(func() { openBrowser = orig })

	// Prime the credentials cache; --console must still call /access for a fresh link.
	if _, _, err := lb.run("creds", "--lease", "L1"); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := lb.run("login", "--lease", "L1", "--console")
	if err != nil {
		t.Fatalf("login --console: %v", err)
	}
	if len(opened) != 1 || !strings.HasPrefix(opened[0], "https://signin.aws.amazon.com/federation?Action=login") {
		t.Errorf("opened = %v", opened)
	}
	if lb.accessCalls() != 2 {
		t.Errorf("access calls = %d, want 2 (console always fetches fresh)", lb.accessCalls())
	}
	if !strings.Contains(stderr, "15 minutes") {
		t.Errorf("stderr should mention the 15-minute sign-in window:\n%s", stderr)
	}
}

func TestCmdLoginFailsClearlyInIDCMode(t *testing.T) {
	lb := newLoginBackend(t, false)
	lb.discovery = `{"status":"success","data":{"issuer":"x","token_endpoint":"y","grant_types_supported":["client_credentials"]}}`
	_, _, err := lb.run("login")
	if err == nil || !strings.Contains(err.Error(), "CLI login requires a Cognito-mode deployment") {
		t.Fatalf("err = %v, want the IDC-mode message", err)
	}
}

func TestCmdWhoamiShowsLoginAuth(t *testing.T) {
	lb := newLoginBackend(t, true)
	stdout, _, err := lb.run("whoami")
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	for _, want := range []string{"u-login", "Admin", "Browser Login"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("whoami missing %q:\n%s", want, stdout)
		}
	}
}

func TestCmdLogoutForgetsSessionAndCredsButKeepsClient(t *testing.T) {
	lb := newLoginBackend(t, true)
	if _, _, err := lb.run("creds", "--lease", "L1"); err != nil {
		t.Fatalf("creds: %v", err)
	}
	_ = lb.mem.Set(auth.KeyClientID, "cid")
	_ = lb.mem.Set(auth.KeyClientSecret, "sec")
	if _, _, err := lb.run("logout"); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := auth.New(lb.mem, lb.url).Session(); err == nil {
		t.Error("the session should be removed by logout")
	}
	if _, ok := credsCache(auth.New(lb.mem, lb.url)).Get("L1", "USER"); ok {
		t.Error("cached lease credentials should be removed by logout")
	}
	if _, err := lb.mem.Get(auth.KeyClientID); err != nil {
		t.Error("plain logout must keep the API client")
	}
	if _, _, err := lb.run("logout", "--all"); err != nil {
		t.Fatalf("logout --all: %v", err)
	}
	if _, err := lb.mem.Get(auth.KeyClientSecret); err == nil {
		t.Error("logout --all should remove the stored API client")
	}
}

func TestCmdLogoutThenCommandsRequireLogin(t *testing.T) {
	lb := newLoginBackend(t, true)
	if _, _, err := lb.run("logout"); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, _, err := lb.run("lease", "list"); err == nil || !strings.Contains(err.Error(), "sbs login") {
		t.Fatalf("err = %v, want login hint after logout", err)
	}
}

func TestCmdConfigureWithoutClientID(t *testing.T) {
	t.Setenv(config.EnvConfigDir, t.TempDir())
	mem := keyring.NewMemory()
	newKeyring = func() keyring.Keyring { return mem }
	t.Cleanup(func() { newKeyring = keyring.New })
	secretReader = func(string) (string, error) {
		t.Error("the secret prompt must not appear when no client id is given")
		return "", nil
	}
	t.Cleanup(func() { secretReader = promptSecret })

	// stdin is empty under `go test`, so the client-id prompt reads "" (blank).
	_, stderr, err := captureStd(func() error {
		flagContext, flagURL, flagOutput, flagDebug = "", "", "", false
		root := newRootCmd()
		root.SetArgs([]string{"configure", "--url", "https://x.example"})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("configure without client id should succeed: %v", err)
	}
	if !strings.Contains(stderr, "sbs login") {
		t.Errorf("expected a hint to run login, got:\n%s", stderr)
	}
	if _, err := mem.Get(auth.KeyClientID); err == nil {
		t.Error("no client id should have been stored")
	}
	cfg, err := config.Load()
	if err != nil || cfg.Contexts["default"].URL != "https://x.example" {
		t.Errorf("url not saved: %+v, %v", cfg, err)
	}
}

func TestCmdWhoamiJSON(t *testing.T) {
	lb := newLoginBackend(t, true)
	stdout, _, err := lb.run("whoami", "--output", "json")
	if err != nil {
		t.Fatalf("whoami -o json: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("whoami -o json did not print JSON: %v\n%s", err, stdout)
	}
	if m["subject"] != "u-login" || m["role"] != "Admin" || m["auth"] != "browser_login" || m["url"] != lb.url {
		t.Errorf("unexpected fields: %v", m)
	}
	if _, err := time.Parse(time.RFC3339, m["expiresAt"].(string)); err != nil {
		t.Errorf("expiresAt not RFC 3339: %v", m["expiresAt"])
	}
}

func stubBrowser(t *testing.T, fail bool) *[]string {
	t.Helper()
	var opened []string
	orig := openBrowser
	openBrowser = func(u string) error {
		opened = append(opened, u)
		if fail {
			return errors.New("no browser")
		}
		return nil
	}
	t.Cleanup(func() { openBrowser = orig })
	return &opened
}

func TestCmdOpenLaunchesConsoleForLease(t *testing.T) {
	lb := newLoginBackend(t, true)
	opened := stubBrowser(t, false)

	stdout, stderr, err := lb.run("open", "L1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(*opened) != 1 || !strings.HasPrefix((*opened)[0], "https://signin.aws.amazon.com/federation?Action=login") {
		t.Errorf("opened = %v", *opened)
	}
	if stdout != "" || !strings.Contains(stderr, "15 minutes") {
		t.Errorf("stdout = %q, stderr = %q", stdout, stderr)
	}
	if lb.accessAuth != "Bearer "+lb.session || lb.accessRoles[0] != "USER" {
		t.Errorf("access auth = %q, roles = %v", lb.accessAuth, lb.accessRoles)
	}

	// Every open fetches a fresh link (the sign-in token expires in ~15m); the
	// `console` alias and --role ADMIN work too.
	if _, _, err := lb.run("console", "L1", "--role", "ADMIN"); err != nil {
		t.Fatalf("console alias: %v", err)
	}
	if lb.accessCalls() != 2 || lb.accessRoles[1] != "ADMIN" || len(*opened) != 2 {
		t.Errorf("calls = %d, roles = %v, opened = %d", lb.accessCalls(), lb.accessRoles, len(*opened))
	}
}

func TestCmdOpenPrintAndJSON(t *testing.T) {
	lb := newLoginBackend(t, true)
	opened := stubBrowser(t, false)

	stdout, _, err := lb.run("open", "L1", "--print")
	if err != nil {
		t.Fatalf("open --print: %v", err)
	}
	if len(*opened) != 0 || !strings.HasPrefix(strings.TrimSpace(stdout), "https://signin.aws.amazon.com/federation") {
		t.Errorf("opened = %v, stdout = %q", *opened, stdout)
	}

	stdout, _, err = lb.run("-o", "json", "open", "L1")
	if err != nil {
		t.Fatalf("open -o json: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil || m["opened"] != true ||
		!strings.HasPrefix(m["url"].(string), "https://signin.aws.amazon.com/") {
		t.Errorf("json = %q (%v)", stdout, err)
	}
}

func TestCmdOpenFallsBackToPrintingTheLink(t *testing.T) {
	lb := newLoginBackend(t, true)
	stubBrowser(t, true)
	stdout, stderr, err := lb.run("open", "L1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout), "https://signin.aws.amazon.com/federation") || !strings.Contains(stderr, "15 minutes") {
		t.Errorf("stdout = %q, stderr = %q", stdout, stderr)
	}
}

func TestCmdOpenErrors(t *testing.T) {
	lb := newLoginBackend(t, true)
	stubBrowser(t, false)
	lb.accessQueue = []accessReply{{409, `{"status":"fail","code":"LEASE_NOT_ACTIVE_FOR_ACCESS","message":"Lease is Suspended"}`}}
	if _, _, err := lb.run("open", "L1"); err == nil || !strings.Contains(err.Error(), "Lease is Suspended") {
		t.Errorf("409: err = %v", err)
	}
	// No lease id and not at a terminal: no picker.
	if _, _, err := lb.run("open"); err == nil || !strings.Contains(err.Error(), "not at a terminal") {
		t.Errorf("no lease: err = %v", err)
	}
	if _, _, err := lb.run("open", "L1", "L2"); err == nil {
		t.Error("two lease ids should be rejected")
	}

	lb2 := newLoginBackend(t, false)
	if _, _, err := lb2.run("open", "L1"); err == nil || !strings.Contains(err.Error(), "sbs login") {
		t.Errorf("not logged in: err = %v", err)
	}
}
