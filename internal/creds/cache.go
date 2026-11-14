package creds

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/api"
	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/keyring"
)

// RefreshWindow is how long before expiration cached credentials stop being
// served: they are reused until 5 minutes before they expire, then re-fetched.
const RefreshWindow = 5 * time.Minute

// Keyring key prefixes. Each cached lease gets its own entry (Windows Credential
// Manager caps an entry at ~2.5 KB, so a single map of sessions would overflow);
// the index lists them so Clear can remove every entry on login/logout.
const (
	keyCredsPrefix = "creds:"
	keyCredsIndex  = "creds-index:"
)

// Cache stores credential_process credentials per (deployment, lease, role) in
// the OS keyring, so repeated `sbs creds` calls (the AWS CLI invokes it often)
// do not hit POST /access every time.
type Cache struct {
	kr  keyring.Keyring
	url string
	now func() time.Time
}

// NewCache returns a credentials cache for one deployment URL.
func NewCache(kr keyring.Keyring, deploymentURL string) *Cache {
	return &Cache{kr: kr, url: deploymentURL, now: time.Now}
}

func (c *Cache) key(leaseID, role string) string {
	return keyCredsPrefix + c.url + "|" + leaseID + "|" + strings.ToUpper(role)
}

// Get returns cached credentials for the lease/role when they are still more than
// RefreshWindow away from expiring.
func (c *Cache) Get(leaseID, role string) (ProcessCredentials, bool) {
	raw, err := c.kr.Get(c.key(leaseID, role))
	if err != nil {
		return ProcessCredentials{}, false
	}
	var pc ProcessCredentials
	if err := json.Unmarshal([]byte(raw), &pc); err != nil || pc.AccessKeyID == "" {
		return ProcessCredentials{}, false
	}
	exp, err := time.Parse(time.RFC3339, pc.Expiration)
	if err != nil || !c.now().Add(RefreshWindow).Before(exp) {
		return ProcessCredentials{}, false
	}
	return pc, true
}

// Put caches credentials for the lease/role. Best-effort: a keyring failure just
// means the next call fetches again.
func (c *Cache) Put(leaseID, role string, pc ProcessCredentials) {
	b, err := json.Marshal(pc)
	if err != nil {
		return
	}
	k := c.key(leaseID, role)
	if err := c.kr.Set(k, string(b)); err != nil {
		return
	}
	idx := c.index()
	for _, e := range idx {
		if e == k {
			return
		}
	}
	c.saveIndex(append(idx, k))
}

// Clear removes every cached credential for the deployment.
func (c *Cache) Clear() {
	for _, k := range c.index() {
		_ = c.kr.Delete(k)
	}
	_ = c.kr.Delete(keyCredsIndex + c.url)
}

func (c *Cache) index() []string {
	raw, err := c.kr.Get(keyCredsIndex + c.url)
	if err != nil {
		return nil
	}
	var keys []string
	_ = json.Unmarshal([]byte(raw), &keys)
	return keys
}

func (c *Cache) saveIndex(keys []string) {
	b, _ := json.Marshal(keys)
	_ = c.kr.Set(keyCredsIndex+c.url, string(b))
}

// AccessFunc fetches lease access (POST /api/leases/{id}/access).
type AccessFunc func(ctx context.Context, leaseID, role string) (*api.LeaseAccess, error)

// Fetch returns credentials for the lease/role from the cache, or fetches them
// with access and caches the result until RefreshWindow before it expires.
func (c *Cache) Fetch(ctx context.Context, leaseID, role string, access AccessFunc) (ProcessCredentials, error) {
	if pc, ok := c.Get(leaseID, role); ok {
		return pc, nil
	}
	a, err := access(ctx, leaseID, role)
	if err != nil {
		return ProcessCredentials{}, err
	}
	if a == nil {
		return ProcessCredentials{}, errNoCredentials
	}
	pc, err := FromAccount(a.Credentials)
	if err != nil {
		return ProcessCredentials{}, err
	}
	// Only cache when the backend stated the expiry; FromAccount's fallback is a
	// guess and must not keep credentials alive past their real lifetime.
	if a.Credentials.Expiration != "" {
		c.Put(leaseID, role, pc)
	}
	return pc, nil
}
