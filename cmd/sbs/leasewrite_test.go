package main

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

const tplID = "11111111-1111-4111-8111-111111111111"

// recorder captures the write requests the CLI makes.
type recorder struct {
	mu    sync.Mutex
	calls []string
	body  map[string]string
}

func (r *recorder) add(method, path, body string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, method+" "+path)
	if r.body == nil {
		r.body = map[string]string{}
	}
	r.body[method+" "+path] = body
}

func (r *recorder) has(call string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.calls {
		if c == call {
			return true
		}
	}
	return false
}

func writeBackend(t *testing.T) (func(args ...string) (string, string, error), *recorder) {
	t.Helper()
	rec := &recorder{}
	run := fakeBackendWith(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/api/templates/public":
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"templateId":"` + tplID + `","name":"Dev Sandbox","maxBudget":50,"maxDurationInHours":8,"requiresApproval":true}],"nextPageIdentifier":null}}`))
		case r.URL.Path == "/api/approvals":
			rec.add(r.Method, r.URL.Path+"?"+r.URL.RawQuery, "")
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"leaseId":"P1","status":"PendingApproval","template":{"name":"Dev Sandbox"},"owner":{"email":"bob@example.com"}}],"nextPageIdentifier":null}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/leases":
			rec.add(r.Method, r.URL.Path, string(b))
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"status":"success","data":{"leaseId":"NEW1","status":"PendingApproval"}}`))
		case r.URL.Path == "/api/leases/L1" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"status":"success","data":{"leaseId":"L1","status":"Active","awsAccountId":"111122223333","template":{"name":"Dev"},"owner":{"email":"me@example.com"}}}`))
		case r.URL.Path == "/api/leases/NEW1" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"status":"success","data":{"leaseId":"NEW1","status":"Active"}}`))
		case r.URL.Path == "/api/leases/L1/team-members" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"status":"success","data":[{"leaseId":"L1","teamMemberUserId":"u9","sharedByUserId":"u1","sharedOn":"2026-01-01T00:00:00Z","userDetails":{"userId":"u9","email":"amy@example.com"}}]}`))
		case r.URL.Path == "/api/leases/L1/logs" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"status":"success","data":{"logs":"step one\nstep two"}}`))
		case r.URL.Path == "/api/leases/FORBIDDEN/review":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"status":"fail","data":{"message":"not an approver"}}`))
		case r.URL.Path == "/api/leases/NOLOGS/logs":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":"fail","data":{"message":"api.errors.LEASE_LOGS_NOT_FOUND"}}`))
		case r.URL.Path == "/api/leases/NOSECRETS/secrets":
			_, _ = w.Write([]byte(`{"status":"success","data":{}}`))
		case r.URL.Path == "/api/leases/L1/secrets" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"status":"success","data":{"Admin Password":"hunter2"}}`))
		case strings.HasPrefix(r.URL.Path, "/api/leases/"):
			rec.add(r.Method, r.URL.Path, string(b))
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"status":"success","data":null}`))
		default:
			http.NotFound(w, r)
		}
	})
	return run, rec
}

func TestCmdLeaseRequestByName(t *testing.T) {
	run, rec := writeBackend(t)
	stdout, _, err := run("lease", "request", "--template", "dev sandbox", "--comments", "hi", "--share-with", "u1,u2")
	if err != nil {
		t.Fatalf("lease request: %v", err)
	}
	if strings.TrimSpace(stdout) != "NEW1" {
		t.Errorf("stdout = %q, want the new lease id", stdout)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(rec.body["POST /api/leases"]), &body); err != nil {
		t.Fatalf("bad request body: %v", err)
	}
	if body["templateId"] != tplID || body["comments"] != "hi" {
		t.Errorf("unexpected body: %v", body)
	}
	if users, _ := body["usersToShareWith"].([]any); len(users) != 2 {
		t.Errorf("usersToShareWith = %v", body["usersToShareWith"])
	}
}

func TestCmdLeaseRequestUnknownTemplate(t *testing.T) {
	run, _ := writeBackend(t)
	if _, _, err := run("lease", "request", "--template", "nope"); err == nil {
		t.Fatal("expected an error for an unknown template name")
	}
	if _, _, err := run("lease", "request"); err == nil {
		t.Fatal("expected an error when --template is missing")
	}
}

func TestCmdLeaseApproveDeny(t *testing.T) {
	run, rec := writeBackend(t)
	if _, _, err := run("lease", "approve", "A1", "A2"); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if _, _, err := run("approval", "deny", "D1"); err != nil {
		t.Fatalf("deny: %v", err)
	}
	if !rec.has("POST /api/leases/A1/review") || !rec.has("POST /api/leases/A2/review") || !rec.has("POST /api/leases/D1/review") {
		t.Fatalf("missing review calls: %v", rec.calls)
	}
	if !strings.Contains(rec.body["POST /api/leases/A1/review"], `"approve"`) ||
		!strings.Contains(rec.body["POST /api/leases/D1/review"], `"deny"`) {
		t.Errorf("wrong actions sent: %v", rec.body)
	}
}

func TestCmdLeaseSimpleActions(t *testing.T) {
	run, rec := writeBackend(t)
	for _, verb := range []string{"suspend", "resume", "withdraw"} {
		if _, _, err := run("lease", verb, "L1"); err != nil {
			t.Fatalf("%s: %v", verb, err)
		}
		if !rec.has("POST /api/leases/L1/" + verb) {
			t.Errorf("%s did not call the API: %v", verb, rec.calls)
		}
	}
}

func TestCmdLeaseTerminateRequiresConfirmation(t *testing.T) {
	run, rec := writeBackend(t)
	// No --yes and no tty/stdin answer => abort without calling the API.
	if _, _, err := run("lease", "terminate", "L1"); err == nil {
		t.Fatal("expected terminate to abort without confirmation")
	}
	if rec.has("POST /api/leases/L1/terminate") {
		t.Fatal("terminate was sent without confirmation")
	}
	if _, _, err := run("lease", "terminate", "L1", "--yes"); err != nil {
		t.Fatalf("terminate --yes: %v", err)
	}
	if !rec.has("POST /api/leases/L1/terminate") {
		t.Fatalf("terminate not sent: %v", rec.calls)
	}
}

func TestCmdLeaseShareUnshare(t *testing.T) {
	run, rec := writeBackend(t)
	if _, _, err := run("lease", "share", "L1", "--user", "u1,u2"); err != nil {
		t.Fatalf("share: %v", err)
	}
	if _, _, err := run("lease", "unshare", "L1", "--user", "u1"); err != nil {
		t.Fatalf("unshare: %v", err)
	}
	if !rec.has("POST /api/leases/L1/team-members") || !rec.has("DELETE /api/leases/L1/team-members") {
		t.Fatalf("unexpected calls: %v", rec.calls)
	}
	if !strings.Contains(rec.body["DELETE /api/leases/L1/team-members"], `"u1"`) {
		t.Errorf("DELETE body missing user: %q", rec.body["DELETE /api/leases/L1/team-members"])
	}
	if _, _, err := run("lease", "share", "L1"); err == nil {
		t.Error("expected an error without --user")
	}
}

func TestCmdLeaseUpdate(t *testing.T) {
	run, rec := writeBackend(t)
	if _, _, err := run("lease", "update", "L1", "--max-budget", "75", "--no-expiry"); err != nil {
		t.Fatalf("update: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(rec.body["PATCH /api/leases/L1"]), &body); err != nil {
		t.Fatalf("bad PATCH body: %v", err)
	}
	if body["maxBudget"] != float64(75) {
		t.Errorf("maxBudget = %v", body["maxBudget"])
	}
	if v, ok := body["expirationDate"]; !ok || v != nil {
		t.Errorf("expirationDate should be sent as null, got %v (present=%v)", v, ok)
	}
	if _, _, err := run("lease", "update", "L1"); err == nil {
		t.Error("expected an error when nothing to update")
	}
}

func TestCmdApprovalListDefaultsToPending(t *testing.T) {
	run, rec := writeBackend(t)
	stdout, _, err := run("approval", "list")
	if err != nil {
		t.Fatalf("approval list: %v", err)
	}
	if !strings.Contains(stdout, "P1") || !strings.Contains(stdout, "bob@example.com") {
		t.Errorf("approval list output missing row:\n%s", stdout)
	}
	if !rec.has("GET /api/approvals?pageSize=200&status=pending") {
		t.Errorf("expected pending filter, calls: %v", rec.calls)
	}
}

func TestCmdTemplateList(t *testing.T) {
	run, _ := writeBackend(t)
	stdout, _, err := run("template", "list")
	if err != nil {
		t.Fatalf("template list: %v", err)
	}
	if !strings.Contains(stdout, "Dev Sandbox") || !strings.Contains(stdout, "required") {
		t.Errorf("template list output unexpected:\n%s", stdout)
	}
}

func TestVersionStringReleaseBuild(t *testing.T) {
	prev := version
	version = "1.2.3"
	t.Cleanup(func() { version = prev })
	if got := versionString(); got != "1.2.3" {
		t.Errorf("versionString = %q, want 1.2.3", got)
	}
}

func TestVersionStringFromFile(t *testing.T) {
	prev := version
	version = ""
	t.Cleanup(func() { version = prev })
	if got := versionString(); !strings.HasPrefix(got, strings.TrimSpace(fileVersion)) || fileVersion == "" {
		t.Errorf("versionString = %q, want prefix from VERSION file %q", got, fileVersion)
	}
}

func TestCmdLeaseWait(t *testing.T) {
	var n int
	run := fakeBackendWith(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/leases/W1" {
			http.NotFound(w, r)
			return
		}
		n++
		st := "PendingSetup"
		if n >= 2 {
			st = "Active"
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"leaseId":"W1","status":"` + st + `"}}`))
	})
	if _, _, err := run("lease", "wait", "W1", "--interval", "10ms", "--timeout", "5s"); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if n < 2 {
		t.Errorf("expected at least 2 polls, got %d", n)
	}
}

func TestCmdLeaseWaitFailsFastOnTerminalStatus(t *testing.T) {
	run := fakeBackendWith(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"leaseId":"W2","status":"ApprovalDenied"}}`))
	})
	_, _, err := run("lease", "wait", "W2", "--interval", "10ms", "--timeout", "5s")
	if err == nil || !strings.Contains(err.Error(), "ApprovalDenied") {
		t.Fatalf("expected an ApprovalDenied error, got %v", err)
	}
}

func TestCmdLeaseWaitTimeout(t *testing.T) {
	run := fakeBackendWith(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"leaseId":"W3","status":"PendingApproval"}}`))
	})
	_, _, err := run("lease", "wait", "W3", "--interval", "10ms", "--timeout", "100ms")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected a timeout error, got %v", err)
	}
}

func TestBrowseRequiresTerminal(t *testing.T) {
	run, _ := writeBackend(t)
	_, _, err := run("lease", "browse")
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("expected a 'needs a terminal' error under test, got %v", err)
	}
}

func TestCmdLeaseGetSuccess(t *testing.T) {
	run, _ := writeBackend(t)
	stdout, _, err := run("lease", "get", "L1")
	if err != nil {
		t.Fatalf("lease get: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(stdout), &m); err != nil {
		t.Fatalf("lease get did not print JSON: %v\n%s", err, stdout)
	}
	if m["leaseId"] != "L1" || m["status"] != "Active" {
		t.Errorf("unexpected lease: %v", m)
	}
}

func TestCmdLeaseTeam(t *testing.T) {
	run, _ := writeBackend(t)
	stdout, _, err := run("lease", "team", "L1")
	if err != nil {
		t.Fatalf("lease team: %v", err)
	}
	if !strings.Contains(stdout, "amy@example.com") || !strings.Contains(stdout, "u9") {
		t.Errorf("team output missing member:\n%s", stdout)
	}
	jsonOut, _, err := run("-o", "json", "lease", "team", "L1")
	if err != nil || !strings.Contains(jsonOut, `"teamMemberUserId": "u9"`) {
		t.Errorf("json team output unexpected (err=%v):\n%s", err, jsonOut)
	}
}

func TestCmdLeaseLogs(t *testing.T) {
	run, _ := writeBackend(t)
	stdout, _, err := run("lease", "logs", "L1")
	if err != nil {
		t.Fatalf("lease logs: %v", err)
	}
	if !strings.Contains(stdout, "step one\nstep two") {
		t.Errorf("logs output unexpected:\n%q", stdout)
	}
}

func TestCmdLeaseSecrets(t *testing.T) {
	run, _ := writeBackend(t)
	stdout, _, err := run("lease", "secrets", "L1")
	if err != nil {
		t.Fatalf("lease secrets: %v", err)
	}
	if !strings.Contains(stdout, "Admin Password") || !strings.Contains(stdout, "hunter2") {
		t.Errorf("secrets output unexpected:\n%s", stdout)
	}
}

func TestCmdLeaseRequestWait(t *testing.T) {
	run, rec := writeBackend(t)
	stdout, _, err := run("lease", "request", "--template", tplID, "--wait")
	if err != nil {
		t.Fatalf("lease request --wait: %v", err)
	}
	if !strings.Contains(stdout, "NEW1") {
		t.Errorf("stdout should contain the lease id: %q", stdout)
	}
	if !rec.has("POST /api/leases") {
		t.Errorf("request was not sent: %v", rec.calls)
	}
}

func TestCmdLeaseListInteractiveNeedsTerminal(t *testing.T) {
	run, _ := writeBackend(t)
	_, _, err := run("lease", "list", "-i")
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("expected a 'needs a terminal' error, got %v", err)
	}
}

func TestParseSince(t *testing.T) {
	if got, err := parseSince("2026-03-04"); err != nil || got != "2026-03-04T00:00:00Z" {
		t.Errorf("date: got %q, %v", got, err)
	}
	if got, err := parseSince("2026-03-04T10:00:00+11:00"); err != nil || got != "2026-03-03T23:00:00Z" {
		t.Errorf("rfc3339: got %q, %v", got, err)
	}
	if got, err := parseSince(""); err != nil || got != "" {
		t.Errorf("empty: got %q, %v", got, err)
	}
	if _, err := parseSince("yesterday"); err == nil {
		t.Error("expected an error for an invalid date")
	}
}

func TestCmdLeaseReviewMessageIsPastTense(t *testing.T) {
	run, _ := writeBackend(t)
	_, stderr, err := run("lease", "deny", "D9")
	if err != nil {
		t.Fatalf("deny: %v", err)
	}
	if !strings.Contains(stderr, "denied") || strings.Contains(stderr, "denyd") {
		t.Errorf("unexpected message: %q", stderr)
	}
	_, stderr, _ = run("lease", "approve", "A9")
	if !strings.Contains(stderr, "approved") {
		t.Errorf("unexpected message: %q", stderr)
	}
}

func TestCmdLeaseLogsNotFoundIsFriendly(t *testing.T) {
	run, _ := writeBackend(t)
	stdout, stderr, err := run("lease", "logs", "NOLOGS")
	if err != nil {
		t.Fatalf("a lease without logs should not be an error: %v", err)
	}
	if stdout != "" || !strings.Contains(stderr, "No setup logs for this lease") {
		t.Errorf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestCmdLeaseSecretsEmptyIsFriendly(t *testing.T) {
	run, _ := writeBackend(t)
	stdout, stderr, err := run("lease", "secrets", "NOSECRETS")
	if err != nil {
		t.Fatalf("lease secrets: %v", err)
	}
	if strings.Contains(stdout, "NAME") || !strings.Contains(stderr, "No secrets for this lease") {
		t.Errorf("stdout=%q stderr=%q", stdout, stderr)
	}
	jsonOut, _, _ := run("-o", "json", "lease", "secrets", "NOSECRETS")
	if strings.TrimSpace(jsonOut) != "{}" {
		t.Errorf("json output = %q, want {}", jsonOut)
	}
}
