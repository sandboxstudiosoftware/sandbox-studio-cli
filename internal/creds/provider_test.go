package creds

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/sandboxstudio/sbs-cli/internal/api"
)

// TestWriteContract asserts the exact JSON schema AWS requires from a
// credential_process: Version MUST be the integer 1 and the four credential
// fields must use AWS's PascalCase key names.
func TestWriteContract(t *testing.T) {
	pc, err := FromAccount(&api.AccountCredentials{
		AccessKeyID:     "AKIAEXAMPLE",
		SecretAccessKey: "secret",
		SessionToken:    "token",
		Expiration:      "2030-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("FromAccount: %v", err)
	}

	var buf bytes.Buffer
	if err := pc.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Must decode into a map with exactly the AWS-specified keys.
	var m map[string]any
	if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
		t.Fatalf("output is not valid JSON: %v (%q)", err, buf.String())
	}

	if v, ok := m["Version"].(float64); !ok || v != 1 {
		t.Errorf("Version: want integer 1, got %v", m["Version"])
	}
	for _, k := range []string{"AccessKeyId", "SecretAccessKey", "SessionToken", "Expiration"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing required key %q in output %q", k, buf.String())
		}
	}

	// Expiration must be RFC3339.
	if _, err := time.Parse(time.RFC3339, m["Expiration"].(string)); err != nil {
		t.Errorf("Expiration is not RFC3339: %v", err)
	}

	// Output must be a single line (trailing newline only).
	if n := bytes.Count(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n")); n != 0 {
		t.Errorf("output must be a single JSON line, found %d embedded newlines", n)
	}
}

func TestFromAccountNil(t *testing.T) {
	if _, err := FromAccount(nil); err == nil {
		t.Fatal("expected an error for nil credentials")
	}
}

func TestFromAccountEmptyExpirationFallsBack(t *testing.T) {
	pc, err := FromAccount(&api.AccountCredentials{AccessKeyID: "a", SecretAccessKey: "b", SessionToken: "c"})
	if err != nil {
		t.Fatalf("FromAccount: %v", err)
	}
	exp, err := time.Parse(time.RFC3339, pc.Expiration)
	if err != nil {
		t.Fatalf("fallback expiration not RFC3339: %v", err)
	}
	if time.Until(exp) <= 0 {
		t.Errorf("fallback expiration should be in the future, got %s", pc.Expiration)
	}
}
