// Package api is a small typed client for the Sandbox Studio HTTP API.
//
// The API wraps successful responses in a JSend-style envelope:
//
//	{ "status": "success", "data": { ... } }
//
// except for the OAuth token endpoint, which returns a raw OAuth2 body. The client
// unwraps the envelope transparently and maps non-2xx responses to *APIError.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is a Sandbox Studio API client. The zero value is not usable; use New.
type Client struct {
	baseURL string
	http    *http.Client
	token   string // bearer JWT; empty for unauthenticated calls (e.g. token exchange)
}

// New returns a client for the given base URL. The "/api" suffix is appended if it
// is not already present, matching how the deployment serves the API.
func New(baseURL, token string) *Client {
	baseURL = strings.TrimRight(baseURL, "/")
	if !strings.HasSuffix(baseURL, "/api") {
		baseURL += "/api"
	}
	return &Client{
		baseURL: baseURL,
		token:   token,
		// API Gateway cuts requests off at 29s; the first sandbox access answers
		// (200 or a retryable 503) within ~20s. 45s leaves room for a cold start.
		http: &http.Client{Timeout: 45 * time.Second},
	}
}

// APIError represents a non-2xx HTTP response.
type APIError struct {
	StatusCode int
	Message    string
	// Code is the machine-readable JSend error code, when the body carried one
	// (e.g. "FEDERATED_ACCESS_PROVISIONING", "LEASE_NOT_ACTIVE_FOR_ACCESS").
	Code string
	// Reference is a support reference some endpoints return with errors.
	Reference string
	Body      string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("api: %d", e.StatusCode)
	if e.Message != "" {
		msg += ": " + e.Message
	}
	if e.Reference != "" {
		msg += " (reference: " + e.Reference + ")"
	}
	return msg
}

// jsendEnvelope is the common success/fail wrapper.
type jsendEnvelope struct {
	Status  string          `json:"status"`
	Data    json.RawMessage `json:"data"`
	Message string          `json:"message"`
}

// do performs a request and, on success, unwraps the JSend envelope into out.
// A nil out discards the body. form controls the content type.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, form bool, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if body != nil {
		if form {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, code, ref := extractError(raw)
		return &APIError{StatusCode: resp.StatusCode, Message: msg, Code: code, Reference: ref, Body: string(raw)}
	}

	if out == nil {
		return nil
	}

	// Attempt to unwrap the JSend envelope; fall back to decoding the raw body for
	// endpoints that are not enveloped (e.g. OAuth token).
	var env jsendEnvelope
	if err := json.Unmarshal(raw, &env); err == nil && env.Status != "" && len(env.Data) > 0 {
		return json.Unmarshal(env.Data, out)
	}
	return json.Unmarshal(raw, out)
}

// extractError pulls a human-readable message, the JSend error code and any
// support reference out of an error body. It understands JSend
// ({status, message, code, data}), the plain {error, reference} shape used by the
// CLI token exchange, and OAuth2 ({error, error_description}).
func extractError(raw []byte) (msg, code, ref string) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", "", ""
	}
	str := func(v any) string { s, _ := v.(string); return s }
	code = str(m["code"])
	ref = str(m["reference"])
	data, _ := m["data"].(map[string]any)
	if code == "" && data != nil {
		code = str(data["code"])
	}
	switch {
	case str(m["error_description"]) != "":
		msg = str(m["error_description"])
	case str(m["error"]) != "":
		msg = str(m["error"])
	case str(m["message"]) != "":
		msg = str(m["message"])
	case data != nil && str(data["message"]) != "":
		msg = str(data["message"])
	}
	return msg, code, ref
}

// getJSON issues a GET and decodes the (unwrapped) response into out.
func (c *Client) getJSON(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, false, out)
}

// sendJSON marshals in (if non-nil) and issues a request with the given method.
func (c *Client) sendJSON(ctx context.Context, method, path string, in, out any) error {
	var r io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	return c.do(ctx, method, path, r, false, out)
}

// patchJSON issues a PATCH with a JSON body.
func (c *Client) patchJSON(ctx context.Context, path string, in, out any) error {
	return c.sendJSON(ctx, http.MethodPatch, path, in, out)
}

// deleteJSON issues a DELETE with a JSON body (used by team-members).
func (c *Client) deleteJSON(ctx context.Context, path string, in, out any) error {
	return c.sendJSON(ctx, http.MethodDelete, path, in, out)
}

// postJSON marshals in as JSON, issues a POST, and decodes the response into out.
func (c *Client) postJSON(ctx context.Context, path string, in, out any) error {
	var r io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	return c.do(ctx, http.MethodPost, path, r, false, out)
}
