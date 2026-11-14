package api

import (
	"context"
	"strings"
	"testing"
)

func TestExchangeCLIIDToken(t *testing.T) {
	c, got := server(t, 200, `{"token":"jwt.abc.def","user":{"userId":"u1"},"expiresAt":"2030-01-01T00:00:00Z"}`)
	sess, err := c.ExchangeCLIIDToken(context.Background(), "the-id-token")
	if err != nil {
		t.Fatalf("ExchangeCLIIDToken: %v", err)
	}
	if sess.Token != "jwt.abc.def" || sess.ExpiresAt != "2030-01-01T00:00:00Z" || string(sess.User) != `{"userId":"u1"}` {
		t.Errorf("session = %+v", sess)
	}
	if got.method != "POST" || got.path != "/api/auth/token-exchange" || got.body != `{"client":"cli","idToken":"the-id-token"}` {
		t.Errorf("request = %s %s %s", got.method, got.path, got.body)
	}
}

func TestExchangeCLIIDTokenErrors(t *testing.T) {
	c, _ := server(t, 401, `{"error":"Invalid token audience"}`)
	_, err := c.ExchangeCLIIDToken(context.Background(), "x")
	ae, ok := err.(*APIError)
	if !ok || ae.StatusCode != 401 || ae.Message != "Invalid token audience" {
		t.Fatalf("err = %#v", err)
	}

	c, _ = server(t, 403, `{"error":"CLI access is disabled","reference":"abc-123"}`)
	_, err = c.ExchangeCLIIDToken(context.Background(), "x")
	ae, ok = err.(*APIError)
	if !ok || ae.StatusCode != 403 || ae.Reference != "abc-123" || !strings.Contains(err.Error(), "CLI access is disabled") ||
		!strings.Contains(err.Error(), "abc-123") {
		t.Fatalf("err = %#v", err)
	}

	c, _ = server(t, 200, `{"user":{}}`)
	if _, err := c.ExchangeCLIIDToken(context.Background(), "x"); err == nil {
		t.Error("an empty token in a 200 response must be an error")
	}
}
