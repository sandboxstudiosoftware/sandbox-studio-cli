package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListLeasesFilterAndUnwrap(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("status") != "Active" {
			t.Errorf("status filter not forwarded: %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"leaseId":"L1","status":"Active","awsAccountId":"111122223333","template":{"name":"Dev"},"owner":{"email":"me@example.com"}}],"nextPageIdentifier":null}}`))
	}))
	defer srv.Close()

	leases, err := New(srv.URL, "t").ListLeases(context.Background(), []string{"Active"})
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	if len(leases) != 1 || leases[0].LeaseID != "L1" || leases[0].AWSAccountID != "111122223333" {
		t.Fatalf("unexpected leases: %+v", leases)
	}
	// Nested template/owner shapes resolve via the label accessors.
	if leases[0].TemplateLabel() != "Dev" {
		t.Errorf("TemplateLabel = %q, want Dev", leases[0].TemplateLabel())
	}
	if leases[0].OwnerLabel() != "me@example.com" {
		t.Errorf("OwnerLabel = %q, want me@example.com", leases[0].OwnerLabel())
	}
}

func TestListLeasesMultipleStatusesCommaJoined(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("status"); got != "Active,Expired" {
			t.Errorf("status = %q, want Active,Expired", got)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[],"nextPageIdentifier":null}}`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "t").ListLeases(context.Background(), []string{"Active", "Expired"}); err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
}

func TestListLeasesNoStatusOmitsFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := r.URL.Query()["status"]; ok {
			t.Errorf("status filter should be absent, got %q", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[],"nextPageIdentifier":null}}`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "t").ListLeases(context.Background(), nil); err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
}

func TestGetLease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/leases/L9" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"leaseId":"L9","status":"Suspended"}}`))
	}))
	defer srv.Close()

	l, err := New(srv.URL, "t").GetLease(context.Background(), "L9")
	if err != nil {
		t.Fatalf("GetLease: %v", err)
	}
	if l.LeaseID != "L9" || l.Status != "Suspended" {
		t.Fatalf("unexpected lease: %+v", l)
	}
}

func TestGetLeaseAccessDefaultsRoleToUser(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Body should contain role=USER when caller passes "".
		var body struct {
			Role string `json:"role"`
		}
		_ = decodeJSONBody(r, &body)
		if body.Role != "USER" {
			t.Errorf("role = %q, want USER (default)", body.Role)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"type":"FEDERATION","credentials":{"accessKeyId":"AK","secretAccessKey":"SK","sessionToken":"ST","expiration":"2030-01-01T00:00:00Z"}}}`))
	}))
	defer srv.Close()

	if _, err := New(srv.URL, "t").GetLeaseAccess(context.Background(), "L1", ""); err != nil {
		t.Fatalf("GetLeaseAccess: %v", err)
	}
}

func TestNetworkErrorSurfaces(t *testing.T) {
	// Point at a closed server to force a transport error.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	if _, err := New(url, "t").ListLeases(context.Background(), nil); err == nil {
		t.Fatal("expected a transport error against a closed server")
	}
}

func TestListLeasesFollowsPagination(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("pageIdentifier") == "" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"leaseId":"A"}],"nextPageIdentifier":"tok"}}`))
			return
		}
		if got := r.URL.Query().Get("pageIdentifier"); got != "tok" {
			t.Errorf("pageIdentifier = %q, want tok", got)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"leaseId":"B"}],"nextPageIdentifier":null}}`))
	}))
	defer srv.Close()

	leases, err := New(srv.URL, "t").ListLeases(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListLeases: %v", err)
	}
	if calls != 2 || len(leases) != 2 || leases[0].LeaseID != "A" || leases[1].LeaseID != "B" {
		t.Fatalf("calls=%d leases=%+v", calls, leases)
	}
}
