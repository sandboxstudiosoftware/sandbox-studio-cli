// Package creds implements the AWS `credential_process` contract.
//
// The AWS CLI and SDKs invoke an external process and read a single JSON object
// from its stdout describing temporary credentials. The schema is fixed by AWS:
//
//	{
//	  "Version": 1,
//	  "AccessKeyId": "...",
//	  "SecretAccessKey": "...",
//	  "SessionToken": "...",
//	  "Expiration": "2024-01-01T00:00:00Z"   // RFC3339
//	}
//
// The credentials come from the Sandbox Studio backend (POST
// /api/leases/{id}/access), which assumes the sandbox role server-side; they last
// the lease template's session duration (up to 12h). The AWS tool shells out again
// automatically when they near expiry. The CLI never calls AWS STS itself.
//
// Contract rules enforced here:
//   - Only the JSON object is written to the provided writer (stdout). All logs
//     must go to stderr by the caller.
//   - Version MUST be the integer 1.
package creds

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/api"
)

// ProcessCredentials is the exact shape AWS expects from a credential_process.
type ProcessCredentials struct {
	Version         int    `json:"Version"`
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
	Expiration      string `json:"Expiration"`
}

var errNoCredentials = errors.New("no credentials returned for this lease")

// FromAccount converts Sandbox Studio account credentials into the AWS
// credential_process shape, normalising the expiration to RFC3339.
func FromAccount(c *api.AccountCredentials) (ProcessCredentials, error) {
	if c == nil {
		return ProcessCredentials{}, errNoCredentials
	}

	exp := c.Expiration
	// Normalise to RFC3339; the API already returns RFC3339 but we re-stamp to be
	// defensive and provide a sane fallback if the field is empty.
	if exp == "" {
		exp = time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	} else if t, err := time.Parse(time.RFC3339, exp); err == nil {
		exp = t.UTC().Format(time.RFC3339)
	}

	return ProcessCredentials{
		Version:         1,
		AccessKeyID:     c.AccessKeyID,
		SecretAccessKey: c.SecretAccessKey,
		SessionToken:    c.SessionToken,
		Expiration:      exp,
	}, nil
}

// Write emits the credentials as a single compact JSON object followed by a
// newline. It writes nothing else, satisfying the credential_process contract.
func (p ProcessCredentials) Write(w io.Writer) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(b))
	return err
}
