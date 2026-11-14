package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

// TokenResponse is the OAuth2 client-credentials token response. It is NOT wrapped
// in the JSend envelope (the backend returns it raw).
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// ExchangeToken performs the OAuth2 client_credentials grant against /oauth/token.
// The resulting JWT is currently valid for 24h server-side.
//
// The client secret is sent in the form body; callers must ensure it is never
// logged. This method does not require the client to carry a bearer token.
func (c *Client) ExchangeToken(ctx context.Context, clientID, clientSecret string) (*TokenResponse, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)

	var out TokenResponse
	// The token endpoint is not JSend-wrapped, so c.do falls through to decoding
	// the raw body into out.
	if err := c.do(ctx, http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()), true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SandboxStudioCLIConfig is the public CLI block the backend publishes on its
// OAuth discovery document. It is absent in IDC mode.
type SandboxStudioCLIConfig struct {
	// LoginPath is the branded login page path, relative to the deployment base
	// URL (e.g. "/login").
	LoginPath string `json:"login_path"`
	// LoopbackPorts are the 127.0.0.1 callback ports the login page accepts; the
	// CLI binds the first free one.
	LoopbackPorts []int `json:"loopback_ports"`
	// APISessionExchange is true when the backend exchanges the login id_token
	// for an API session (POST /api/auth/token-exchange with client "cli").
	APISessionExchange bool `json:"api_session_exchange"`
}

// WellKnownOAuth is the unauthenticated OAuth authorization-server metadata.
//
// Issuer and TokenEndpoint may point at the raw API Gateway host, so the CLI
// never derives its base URL from them.
type WellKnownOAuth struct {
	Issuer              string                  `json:"issuer"`
	TokenEndpoint       string                  `json:"token_endpoint"`
	GrantTypesSupported []string                `json:"grant_types_supported"`
	SandboxStudioCLI    *SandboxStudioCLIConfig `json:"sandbox_studio_cli,omitempty"`
}

// WellKnownOAuthConfig fetches the public OAuth discovery document
// (GET /api/oauth/.well-known/oauth-authorization-server). No authentication.
func (c *Client) WellKnownOAuthConfig(ctx context.Context) (*WellKnownOAuth, error) {
	var out WellKnownOAuth
	if err := c.getJSON(ctx, "/oauth/.well-known/oauth-authorization-server", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CLISession is the response of POST /api/auth/token-exchange with client "cli":
// an API session JWT obtained by exchanging the login id_token. It is NOT
// JSend-wrapped.
type CLISession struct {
	Token     string          `json:"token"`
	User      json.RawMessage `json:"user,omitempty"`
	ExpiresAt string          `json:"expiresAt"`
}

// ExchangeCLIIDToken swaps the Cognito id_token delivered by the branded login
// page for an API session token. The endpoint is unauthenticated; the id_token is
// the credential. Errors ({"error": "...", "reference"?: "..."} with
// 400/401/403) are returned as *APIError.
//
// "client": "cli" is required: without it the backend issues a web session (a
// different length, and no expiresAt).
func (c *Client) ExchangeCLIIDToken(ctx context.Context, idToken string) (*CLISession, error) {
	var out CLISession
	if err := c.postJSON(ctx, "/auth/token-exchange", map[string]string{"idToken": idToken, "client": "cli"}, &out); err != nil {
		return nil, err
	}
	if out.Token == "" {
		return nil, &APIError{StatusCode: 502, Message: "empty token in token-exchange response"}
	}
	return &out, nil
}
