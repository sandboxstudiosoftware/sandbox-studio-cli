package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/sandboxstudio/sbs-cli/internal/api"
	"github.com/sandboxstudio/sbs-cli/internal/auth"
	"github.com/sandboxstudio/sbs-cli/internal/config"
	"github.com/sandboxstudio/sbs-cli/internal/creds"
	"github.com/sandboxstudio/sbs-cli/internal/keyring"
)

// newKeyring constructs the keyring used by commands. It is a package var so tests
// can substitute an in-memory fake.
var newKeyring = keyring.New

// resolve loads the config, picks the active context (honouring --url and
// --context), and returns the context plus an Authenticator ready to produce an
// API client.
func resolve() (config.Context, *auth.Authenticator, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Context{}, nil, err
	}

	ctx, err := cfg.Current(flagContext)
	if err != nil && flagURL == "" {
		return config.Context{}, nil, err
	}
	if flagURL != "" {
		ctx.URL = flagURL
	}

	kr := newKeyring()
	return ctx, auth.New(kr, ctx.URL), nil
}

// interactiveTerminal reports whether both stdin and stderr are terminals (stdout
// is deliberately not required so `-o json | jq` works). It is a package var so
// tests can force it.
var interactiveTerminal = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stderr.Fd()))
}

// resolveLease returns the lease id to act on, falling back to the context's
// configured default lease when the flag is empty.
func resolveLease(flagLease string, ctx config.Context) string {
	if flagLease != "" {
		return flagLease
	}
	return ctx.DefaultLease
}

// roleFlagHelp is the shared help text for --role.
const roleFlagHelp = "access role: USER (default; credentials last the template's session duration, up to 12h) " +
	"or ADMIN (managers/admins only: shared full-admin role, at most 1h)"

// normalizeRole validates the --role flag value.
func normalizeRole(role string) (string, error) {
	switch r := strings.ToUpper(strings.TrimSpace(role)); r {
	case "", api.AccessRoleUser:
		return api.AccessRoleUser, nil
	case api.AccessRoleAdmin:
		return api.AccessRoleAdmin, nil
	default:
		return "", fmt.Errorf("invalid --role %q: use USER or ADMIN", role)
	}
}

// credsCache returns the per-lease credentials cache for the authenticator's
// deployment.
func credsCache(a *auth.Authenticator) *creds.Cache {
	return creds.NewCache(newKeyring(), a.URL())
}

// leaseAccess calls POST /api/leases/{id}/access (which retries the 503
// FEDERATED_ACCESS_PROVISIONING response itself) and maps failures to actionable
// errors.
func leaseAccess(ctx context.Context, a *auth.Authenticator, c *api.Client, leaseID, role string) (*api.LeaseAccess, error) {
	access, err := c.GetLeaseAccess(ctx, leaseID, role)
	if err != nil {
		return nil, accessError(a, leaseID, err)
	}
	return access, nil
}

// accessError maps a POST /access failure to a user-facing error.
func accessError(a *auth.Authenticator, leaseID string, err error) error {
	var ae *api.APIError
	if !errors.As(err, &ae) {
		return err
	}
	msg := ae.Message
	if msg == "" {
		msg = ae.Error()
	}
	switch {
	case ae.StatusCode == 401 || ae.StatusCode == 403:
		return a.MapAccessError(err)
	case ae.StatusCode == 409:
		return fmt.Errorf("lease %s is not active: %s", leaseID, msg)
	case api.IsProvisioning(err):
		return fmt.Errorf("access to lease %s is still being provisioned; try again shortly: %s", leaseID, msg)
	}
	return err
}

// leaseCredentials returns credential_process credentials for a lease, from the
// keyring cache when they are more than 5 minutes from expiring, otherwise from
// POST /access.
func leaseCredentials(ctx context.Context, a *auth.Authenticator, leaseID, role string) (creds.ProcessCredentials, error) {
	fetched := false
	pc, err := credsCache(a).Fetch(ctx, leaseID, role, func(ctx context.Context, leaseID, role string) (*api.LeaseAccess, error) {
		fetched = true
		// The API client (and so a valid session) is only needed on a cache miss.
		client, err := a.Client(ctx)
		if err != nil {
			return nil, err
		}
		debugf("requesting credentials for lease %s (%s)", leaseID, role)
		return leaseAccess(ctx, a, client, leaseID, role)
	})
	if err == nil && !fetched {
		debugf("using cached credentials for lease %s (%s), expiring %s", leaseID, role, pc.Expiration)
	}
	return pc, err
}

// debugf writes a debug line to stderr when --debug is set.
func debugf(format string, args ...any) {
	if flagDebug {
		fmt.Fprintf(os.Stderr, "debug: "+format+"\n", args...)
	}
}
