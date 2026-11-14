package creds

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/api"
	"github.com/sandboxstudiosoftware/sandbox-studio-cli/internal/keyring"
)

// fakeAccess counts calls and returns credentials expiring at exp.
type fakeAccess struct {
	calls int
	roles []string
	exp   string
	err   error
}

func (f *fakeAccess) fn(_ context.Context, leaseID, role string) (*api.LeaseAccess, error) {
	f.calls++
	f.roles = append(f.roles, role)
	if f.err != nil {
		return nil, f.err
	}
	return &api.LeaseAccess{Type: "FEDERATION", Credentials: &api.AccountCredentials{
		AccessKeyID: "AK-" + leaseID, SecretAccessKey: "SK", SessionToken: "ST", Expiration: f.exp,
	}}, nil
}

func TestCacheServesUntilFiveMinutesBeforeExpiry(t *testing.T) {
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	c := NewCache(keyring.NewMemory(), "https://x.example.com")
	c.now = func() time.Time { return now }
	f := &fakeAccess{exp: now.Add(12 * time.Hour).Format(time.RFC3339)}

	pc, err := c.Fetch(context.Background(), "L1", "USER", f.fn)
	if err != nil || pc.AccessKeyID != "AK-L1" || pc.Version != 1 || pc.Expiration != "2030-01-01T12:00:00Z" {
		t.Fatalf("Fetch = %+v, %v", pc, err)
	}
	// Served from the cache up to (not including) 5 minutes before expiry.
	now = now.Add(12*time.Hour - 5*time.Minute - time.Second)
	if _, err := c.Fetch(context.Background(), "L1", "USER", f.fn); err != nil || f.calls != 1 {
		t.Fatalf("calls = %d, err = %v; want a cache hit", f.calls, err)
	}
	now = now.Add(time.Second)
	if _, err := c.Fetch(context.Background(), "L1", "USER", f.fn); err != nil || f.calls != 2 {
		t.Fatalf("calls = %d, err = %v; want a refetch inside the 5-minute window", f.calls, err)
	}
}

func TestCacheIsPerLeaseAndRole(t *testing.T) {
	c := NewCache(keyring.NewMemory(), "https://x.example.com")
	f := &fakeAccess{exp: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	for _, k := range [][2]string{{"L1", "USER"}, {"L2", "USER"}, {"L1", "ADMIN"}, {"L1", "user"}} {
		if _, err := c.Fetch(context.Background(), k[0], k[1], f.fn); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls != 3 {
		t.Errorf("calls = %d, want 3 (role is case-insensitive)", f.calls)
	}
	other := NewCache(c.kr, "https://other.example.com")
	if _, ok := other.Get("L1", "USER"); ok {
		t.Error("the cache must be per deployment URL")
	}
}

func TestCacheClear(t *testing.T) {
	kr := keyring.NewMemory()
	c := NewCache(kr, "https://x.example.com")
	f := &fakeAccess{exp: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}
	_, _ = c.Fetch(context.Background(), "L1", "USER", f.fn)
	_, _ = c.Fetch(context.Background(), "L2", "ADMIN", f.fn)
	c.Clear()
	if _, ok := c.Get("L1", "USER"); ok {
		t.Error("L1 still cached after Clear")
	}
	if _, ok := c.Get("L2", "ADMIN"); ok {
		t.Error("L2 still cached after Clear")
	}
	if _, err := kr.Get(keyCredsIndex + "https://x.example.com"); err == nil {
		t.Error("index should be removed")
	}
}

func TestCacheDoesNotCacheErrorsOrUnknownExpiry(t *testing.T) {
	c := NewCache(keyring.NewMemory(), "https://x.example.com")
	f := &fakeAccess{err: errors.New("boom")}
	if _, err := c.Fetch(context.Background(), "L1", "USER", f.fn); err == nil {
		t.Fatal("expected the access error")
	}
	f.err = nil // no expiration from the backend
	for i := 0; i < 2; i++ {
		if _, err := c.Fetch(context.Background(), "L1", "USER", f.fn); err != nil {
			t.Fatal(err)
		}
	}
	if f.calls != 3 {
		t.Errorf("calls = %d, want 3 (nothing cached)", f.calls)
	}
}
