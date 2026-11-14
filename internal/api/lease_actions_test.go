package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// capture starts a server that records the last request and replies with body.
type capture struct {
	method, path, query, body string
}

func server(t *testing.T, status int, reply string) (*Client, *capture) {
	t.Helper()
	c := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.method, c.path, c.query, c.body = r.Method, r.URL.Path, r.URL.RawQuery, string(b)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "tok"), c
}

func TestRequestLease(t *testing.T) {
	c, got := server(t, 201, `{"status":"success","data":{"leaseId":"N1","status":"PendingApproval"}}`)
	l, err := c.RequestLease(context.Background(), NewLeaseRequest{TemplateID: "T", Comments: "hi", UsersToShareWith: []string{"u1"}})
	if err != nil {
		t.Fatalf("RequestLease: %v", err)
	}
	if l.LeaseID != "N1" || l.Status != "PendingApproval" {
		t.Errorf("lease = %+v", l)
	}
	if got.method != "POST" || got.path != "/api/leases" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(got.body), &body)
	if body["templateId"] != "T" || body["comments"] != "hi" {
		t.Errorf("body = %v", body)
	}
	if _, ok := body["leaseForUserId"]; ok {
		t.Error("empty leaseForUserId must be omitted (the API schema is strict)")
	}
}

func TestLeaseActionsHitTheRightEndpoints(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		call func(c *Client) error
		path string
	}{
		{"terminate", func(c *Client) error { return c.TerminateLease(ctx, "L1") }, "/api/leases/L1/terminate"},
		{"suspend", func(c *Client) error { return c.SuspendLease(ctx, "L1") }, "/api/leases/L1/suspend"},
		{"resume", func(c *Client) error { return c.ResumeLease(ctx, "L1") }, "/api/leases/L1/resume"},
		{"withdraw", func(c *Client) error { return c.WithdrawLease(ctx, "L1") }, "/api/leases/L1/withdraw"},
	}
	for _, tc := range cases {
		c, got := server(t, 200, `{"status":"success","data":null}`)
		if err := tc.call(c); err != nil {
			t.Errorf("%s: %v", tc.name, err)
		}
		if got.method != "POST" || got.path != tc.path {
			t.Errorf("%s: request = %s %s, want POST %s", tc.name, got.method, got.path, tc.path)
		}
	}
}

func TestReviewLeaseSendsAction(t *testing.T) {
	c, got := server(t, 200, `{"status":"success","data":null}`)
	if err := c.ReviewLease(context.Background(), "L1", "deny"); err != nil {
		t.Fatalf("ReviewLease: %v", err)
	}
	if got.path != "/api/leases/L1/review" || got.body != `{"action":"deny"}` {
		t.Errorf("request = %s body %s", got.path, got.body)
	}
}

func TestUpdateLeaseSendsNullToClear(t *testing.T) {
	c, got := server(t, 200, `{"status":"success","data":{"leaseId":"L1","status":"Active"}}`)
	if _, err := c.UpdateLease(context.Background(), "L1", map[string]any{"maxBudget": nil, "maxDurationInHours": 4}); err != nil {
		t.Fatalf("UpdateLease: %v", err)
	}
	if got.method != "PATCH" || got.path != "/api/leases/L1" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(got.body), &body)
	if v, ok := body["maxBudget"]; !ok || v != nil {
		t.Errorf("maxBudget should be an explicit null, body = %s", got.body)
	}
	if body["maxDurationInHours"] != float64(4) {
		t.Errorf("body = %s", got.body)
	}
}

func TestShareAndUnshare(t *testing.T) {
	c, got := server(t, 202, `{"status":"success","data":null}`)
	if err := c.ShareLease(context.Background(), "L1", []string{"a", "b"}); err != nil {
		t.Fatalf("ShareLease: %v", err)
	}
	if got.method != "POST" || got.path != "/api/leases/L1/team-members" || got.body != `{"userIds":["a","b"]}` {
		t.Errorf("share request = %s %s %s", got.method, got.path, got.body)
	}
	if err := c.UnshareLease(context.Background(), "L1", []string{"a"}); err != nil {
		t.Fatalf("UnshareLease: %v", err)
	}
	if got.method != "DELETE" || got.body != `{"userIds":["a"]}` {
		t.Errorf("unshare request = %s %s", got.method, got.body)
	}
}

func TestTeamMembersLogsSecrets(t *testing.T) {
	ctx := context.Background()
	c, _ := server(t, 200, `{"status":"success","data":[{"teamMemberUserId":"u9","userDetails":{"email":"amy@example.com"}},{"teamMemberUserId":"u8"}]}`)
	members, err := c.LeaseTeamMembers(ctx, "L1")
	if err != nil || len(members) != 2 {
		t.Fatalf("LeaseTeamMembers: %v %+v", err, members)
	}
	if members[0].Label() != "amy@example.com" || members[1].Label() != "u8" {
		t.Errorf("labels = %q, %q", members[0].Label(), members[1].Label())
	}

	c, _ = server(t, 200, `{"status":"success","data":{"logs":"hello"}}`)
	if logs, err := c.LeaseLogs(ctx, "L1"); err != nil || logs != "hello" {
		t.Errorf("LeaseLogs = %q, %v", logs, err)
	}

	c, _ = server(t, 200, `{"status":"success","data":{"k":"v"}}`)
	if s, err := c.LeaseSecrets(ctx, "L1"); err != nil || s["k"] != "v" {
		t.Errorf("LeaseSecrets = %v, %v", s, err)
	}
}

func TestListApprovalsStatusAndPaging(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/approvals" || r.URL.Query().Get("status") != "pending,approved" {
			t.Errorf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if r.URL.Query().Get("pageIdentifier") == "" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"leaseId":"A"}],"nextPageIdentifier":"p2"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"leaseId":"B"}],"nextPageIdentifier":null}}`))
	}))
	defer srv.Close()
	leases, err := New(srv.URL, "t").ListApprovals(context.Background(), []string{"pending", "approved"})
	if err != nil || len(leases) != 2 || calls != 2 {
		t.Fatalf("ListApprovals = %+v, %v (calls=%d)", leases, err, calls)
	}
}

func TestListLeasesWithUserAndStartDate(t *testing.T) {
	c, got := server(t, 200, `{"status":"success","data":{"result":[],"nextPageIdentifier":null}}`)
	_, err := c.ListLeasesWith(context.Background(), ListLeasesOptions{
		Statuses: []string{"Active"}, UserID: "u1", StartDate: "2026-01-02T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("ListLeasesWith: %v", err)
	}
	for _, want := range []string{"userId=u1", "startDate=2026-01-02T00%3A00%3A00Z", "status=Active"} {
		if !contains(got.query, want) {
			t.Errorf("query %q missing %q", got.query, want)
		}
	}
}

func TestListPublicTemplatesPaging(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("pageIdentifier") == "" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"templateId":"T1","name":"A"}],"nextPageIdentifier":"n"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"result":[{"templateId":"T2","name":"B","requiresApproval":true}],"nextPageIdentifier":null}}`))
	}))
	defer srv.Close()
	ts, err := New(srv.URL, "t").ListPublicTemplates(context.Background())
	if err != nil || len(ts) != 2 || !ts[1].RequiresApproval || calls != 2 {
		t.Fatalf("ListPublicTemplates = %+v, %v (calls=%d)", ts, err, calls)
	}
}

func TestWriteCallsSurfaceAPIErrors(t *testing.T) {
	c, _ := server(t, 403, `{"status":"fail","data":{"message":"not an approver"}}`)
	err := c.ReviewLease(context.Background(), "L1", "approve")
	ae, ok := err.(*APIError)
	if !ok || ae.StatusCode != 403 || ae.Message != "not an approver" {
		t.Fatalf("err = %#v", err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
