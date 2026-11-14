package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/output"
)

// roleColour styles an application role for display.
func roleColour(role string) string {
	switch strings.ToLower(role) {
	case "admin":
		return output.Magenta(role)
	case "manager":
		return output.Blue(role)
	case "user":
		return output.Green(role)
	default:
		return output.Dash(role)
	}
}

func newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the resolved context, token subject and expiry",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, a, err := resolve()
			if err != nil {
				return err
			}
			tok, err := a.Token(context.Background())
			if err != nil {
				return err
			}

			sub, role, exp := decodeClaims(tok)
			if flagOutput == "json" {
				method := "browser_login"
				if a.UsesClientCredentials() {
					method = "api_client"
				}
				out := map[string]any{"url": ctx.URL, "subject": sub, "role": role, "auth": method}
				if !exp.IsZero() {
					out["expiresAt"] = exp.Format(time.RFC3339)
				}
				return printJSON(out)
			}
			fmt.Println(output.KeyValue("URL    ", ctx.URL))
			fmt.Println(output.KeyValue("Subject", output.Cyan(orNA(sub))))
			fmt.Println(output.KeyValue("Role   ", roleColour(role)))
			method := output.Yellow("Browser Login")
			if a.UsesClientCredentials() {
				method = "API client"
			}
			fmt.Println(output.KeyValue("Auth   ", method))
			if !exp.IsZero() {
				fmt.Println(
					output.KeyValue(
						"Expires",
						fmt.Sprintf("%s %s", exp.Format(time.RFC3339),
							output.Faint("(in "+time.Until(exp).Round(time.Second).String()+")")),
					),
				)
			}
			return nil
		},
	}
}

// decodeClaims extracts display fields from the Sandbox Studio API JWT without
// verifying it (the server verifies on every call). The API token nests the
// user under a "user" object: {user:{userId,email,role,...}, apiClientId, exp}.
func decodeClaims(tok string) (sub, role string, exp time.Time) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return
	}
	var claims struct {
		User struct {
			UserID string `json:"userId"`
			Email  string `json:"email"`
			Role   string `json:"role"`
		} `json:"user"`
		// Fall back to top-level fields for other token shapes (e.g. a raw
		// Cognito id_token).
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Role  string `json:"role"`
		Exp   int64  `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return
	}
	sub = firstNonEmpty(claims.User.UserID, claims.User.Email, claims.Sub, claims.Email)
	role = firstNonEmpty(claims.User.Role, claims.Role)
	if claims.Exp != 0 {
		exp = time.Unix(claims.Exp, 0)
	}
	return
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func orNA(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}
