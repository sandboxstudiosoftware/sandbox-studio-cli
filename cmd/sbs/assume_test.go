package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sandboxstudio/sbs-cli/internal/creds"
)

func TestCmdAssumeExportPosix(t *testing.T) {
	run := fakeBackend(t)
	stdout, _, err := run("assume", "L1", "--static", "--export", "--shell", "posix")
	if err != nil {
		t.Fatalf("assume: %v", err)
	}
	for _, want := range []string{
		"export AWS_ACCESS_KEY_ID='ASIAFAKE'\n",
		"export AWS_SECRET_ACCESS_KEY='sk'\n",
		"export AWS_SESSION_TOKEN='st'\n",
		"export SBS_LEASE_ID='L1'\n",
		"unset AWS_PROFILE\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, stdout)
		}
	}
}

func TestCmdAssumeStaticRunsCommandWithEnv(t *testing.T) {
	run := fakeBackend(t)
	t.Setenv("AWS_PROFILE", "should-be-removed")
	stdout, _, err := run("assume", "L1", "--static", "--", "sh", "-c", `echo "$AWS_ACCESS_KEY_ID|$AWS_SESSION_TOKEN|$SBS_LEASE_ID|${AWS_PROFILE:-unset}"`)
	if err != nil {
		t.Fatalf("assume: %v", err)
	}
	if got := strings.TrimSpace(stdout); got != "ASIAFAKE|st|L1|unset" {
		t.Errorf("child env = %q", got)
	}
}

func TestCmdAssumePropagatesExitCode(t *testing.T) {
	run := fakeBackend(t)
	_, _, err := run("assume", "L1", "--", "sh", "-c", "exit 7")
	ec, ok := err.(*exitCodeError)
	if !ok || ec.code != 7 {
		t.Fatalf("err = %#v, want exitCodeError{7}", err)
	}
}

func TestCmdAssumeNoLeaseNeedsTerminal(t *testing.T) {
	run := fakeBackend(t)
	_, _, err := run("assume")
	if err == nil || !strings.Contains(err.Error(), "not at a terminal") {
		t.Fatalf("err = %v, want a terminal hint", err)
	}
}

func TestExportScriptQuotingAndShells(t *testing.T) {
	e := buildAssumeEnv(creds.ProcessCredentials{AccessKeyID: "a", SecretAccessKey: "it's", SessionToken: "t", Expiration: "x"}, "L")
	posix, _ := e.exportScript("posix")
	if !strings.Contains(posix, `export AWS_SECRET_ACCESS_KEY='it'\''s'`) || !strings.Contains(posix, "SBS_LEASE_ID='L'") {
		t.Errorf("posix:\n%s", posix)
	}
	fish, _ := e.exportScript("fish")
	if !strings.Contains(fish, `set -gx AWS_SECRET_ACCESS_KEY 'it\'s'`) || !strings.Contains(fish, "set -e AWS_PROFILE") {
		t.Errorf("fish:\n%s", fish)
	}
	ps, _ := e.exportScript("powershell")
	if !strings.Contains(ps, `$env:AWS_SECRET_ACCESS_KEY = 'it''s'`) {
		t.Errorf("powershell:\n%s", ps)
	}
	if _, err := e.exportScript("nope"); err == nil {
		t.Error("unknown shell should error")
	}
}

func TestApplyReplacesAndRemovesVars(t *testing.T) {
	e := buildAssumeEnv(creds.ProcessCredentials{AccessKeyID: "new"}, "L")
	out := e.apply([]string{"PATH=/bin", "AWS_ACCESS_KEY_ID=old", "AWS_PROFILE=p"})
	joined := strings.Join(out, " ")
	if strings.Contains(joined, "old") || strings.Contains(joined, "AWS_PROFILE") || !strings.Contains(joined, "AWS_ACCESS_KEY_ID=new") || !strings.Contains(joined, "PATH=/bin") {
		t.Errorf("env = %v", out)
	}
}

// TestCmdAssumeDefaultUsesRefreshingProfile asserts the default mode exposes a
// credential_process profile (auto-refreshing) instead of static keys, and keeps
// the user's existing AWS config profiles.
func TestCmdAssumeDefaultUsesRefreshingProfile(t *testing.T) {
	run := fakeBackend(t)
	userCfg := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(userCfg, []byte("[profile other]\nregion = us-east-1"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", userCfg)
	t.Setenv("AWS_ACCESS_KEY_ID", "STALE")
	orig := sbsExecutable
	sbsExecutable = func() (string, error) { return "/opt/my tools/sbs", nil }
	t.Cleanup(func() { sbsExecutable = orig })

	stdout, _, err := run("assume", "L1", "--", "sh", "-c",
		`echo "${AWS_ACCESS_KEY_ID:-nokey}|$AWS_PROFILE|$AWS_CONFIG_FILE"`)
	if err != nil {
		t.Fatalf("assume: %v", err)
	}
	parts := strings.Split(strings.TrimSpace(stdout), "|")
	if len(parts) != 3 || parts[0] != "nokey" || parts[1] != "sbs-L1" {
		t.Fatalf("child env = %q", stdout)
	}
	data, err := os.ReadFile(parts[2])
	if err != nil {
		t.Fatalf("reading generated config: %v", err)
	}
	cfg := string(data)
	for _, want := range []string{
		"[profile other]\nregion = us-east-1\n",
		"[profile sbs-L1]\n",
		`credential_process = "/opt/my tools/sbs" creds --lease L1`,
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("generated config missing %q:\n%s", want, cfg)
		}
	}
	if fi, _ := os.Stat(parts[2]); fi.Mode().Perm() != 0o600 {
		t.Errorf("config perms = %v, want 0600", fi.Mode().Perm())
	}
}

func TestCredentialProcessCommandCarriesFlags(t *testing.T) {
	flagContext, flagURL = "work", "https://x.example"
	t.Cleanup(func() { flagContext, flagURL = "", "" })
	got := credentialProcessCommand("/usr/local/bin/sbs", "L1", "ADMIN")
	want := "/usr/local/bin/sbs creds --lease L1 --context work --url https://x.example --role ADMIN"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}
