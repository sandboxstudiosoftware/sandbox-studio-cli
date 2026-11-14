package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every non-interactive command must emit exactly one valid JSON document on
// stdout when run with `-o json`, so scripts can always pipe it to jq.
func TestAllCommandsEmitJSONWithOutputJSON(t *testing.T) {
	run, _ := writeBackend(t)
	cases := [][]string{
		{"version"},
		{"whoami"},
		{"configure", "--url", "https://x.example"},
		{"logout"},
		{"creds", "--lease", "L1"},
		{"login", "--lease", "L1"},
		{"lease", "list"},
		{"lease", "get", "L1"},
		{"lease", "request", "--template", tplID},
		{"lease", "approve", "A1"},
		{"lease", "deny", "D1"},
		{"lease", "withdraw", "L1"},
		{"lease", "suspend", "L1"},
		{"lease", "resume", "L1"},
		{"lease", "terminate", "L1", "--yes"},
		{"lease", "share", "L1", "--user", "u1"},
		{"lease", "unshare", "L1", "--user", "u1"},
		{"lease", "team", "L1"},
		{"lease", "update", "L1", "--max-budget", "5"},
		{"lease", "logs", "L1"},
		{"lease", "logs", "NOLOGS"},
		{"lease", "secrets", "L1"},
		{"lease", "secrets", "NOSECRETS"},
		{"lease", "wait", "L1"},
		{"approval", "list"},
		{"approval", "approve", "A2"},
		{"template", "list"},
	}
	for _, args := range cases {
		stdout, _, err := run(append([]string{"-o", "json"}, args...)...)
		if err != nil {
			t.Errorf("%s: %v", strings.Join(args, " "), err)
			continue
		}
		dec := json.NewDecoder(strings.NewReader(stdout))
		var v any
		if err := dec.Decode(&v); err != nil {
			t.Errorf("%s: stdout is not JSON: %v\n%q", strings.Join(args, " "), err, stdout)
			continue
		}
		if dec.More() {
			t.Errorf("%s: stdout has more than one JSON document:\n%q", strings.Join(args, " "), stdout)
		}
	}
}

func TestJSONActionResultsShape(t *testing.T) {
	run, _ := writeBackend(t)
	stdout, _, err := run("-o", "json", "lease", "suspend", "L1", "L2")
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	var res []actionResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("not an action result array: %v\n%s", err, stdout)
	}
	if len(res) != 2 || res[0].LeaseID != "L1" || res[1].LeaseID != "L2" || !res[0].OK || res[0].Action != "suspend" {
		t.Errorf("results = %+v", res)
	}
}

func TestJSONActionFailureStillPrintsResultsAndFails(t *testing.T) {
	run, _ := writeBackend(t)
	stdout, _, err := run("-o", "json", "lease", "approve", "A1", "FORBIDDEN")
	if err == nil {
		t.Fatal("expected a non-zero exit when one review fails")
	}
	var res []actionResult
	if jerr := json.Unmarshal([]byte(stdout), &res); jerr != nil {
		t.Fatalf("failure output is not JSON: %v\n%s", jerr, stdout)
	}
	if len(res) != 2 || !res[0].OK || res[1].OK || !strings.Contains(res[1].Error, "not an approver") {
		t.Errorf("results = %+v", res)
	}
}

func TestInvalidOutputFormatIsRejected(t *testing.T) {
	run, _ := writeBackend(t)
	_, _, err := run("-o", "yaml", "lease", "list")
	if err == nil || !strings.Contains(err.Error(), "invalid --output") {
		t.Fatalf("err = %v, want an invalid --output error", err)
	}
	if _, _, err := run("-o", "table", "lease", "list"); err != nil {
		t.Errorf("-o table should be accepted: %v", err)
	}
}
