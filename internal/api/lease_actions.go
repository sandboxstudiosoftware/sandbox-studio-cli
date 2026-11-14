package api

import (
	"context"
	"net/url"
	"strings"
)

// NewLeaseRequest is the body of POST /leases.
type NewLeaseRequest struct {
	TemplateID       string   `json:"templateId"`
	Comments         string   `json:"comments,omitempty"`
	UsersToShareWith []string `json:"usersToShareWith,omitempty"`
	LeaseForUserID   string   `json:"leaseForUserId,omitempty"`
}

// RequestLease submits a new lease request. Depending on the template it is
// either provisioned straight away or left in PendingApproval.
func (c *Client) RequestLease(ctx context.Context, req NewLeaseRequest) (*Lease, error) {
	var out Lease
	if err := c.postJSON(ctx, "/leases", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReviewLease approves ("approve") or denies ("deny") a pending lease request.
func (c *Client) ReviewLease(ctx context.Context, leaseID, action string) error {
	return c.postJSON(ctx, "/leases/"+url.PathEscape(leaseID)+"/review",
		map[string]string{"action": action}, nil)
}

// TerminateLease ends a lease immediately; the account is then cleaned.
func (c *Client) TerminateLease(ctx context.Context, leaseID string) error {
	return c.postJSON(ctx, "/leases/"+url.PathEscape(leaseID)+"/terminate", nil, nil)
}

// SuspendLease temporarily revokes access to a lease's account.
func (c *Client) SuspendLease(ctx context.Context, leaseID string) error {
	return c.postJSON(ctx, "/leases/"+url.PathEscape(leaseID)+"/suspend", nil, nil)
}

// ResumeLease restores access to a suspended lease.
func (c *Client) ResumeLease(ctx context.Context, leaseID string) error {
	return c.postJSON(ctx, "/leases/"+url.PathEscape(leaseID)+"/resume", nil, nil)
}

// WithdrawLease cancels your own pending lease request.
func (c *Client) WithdrawLease(ctx context.Context, leaseID string) error {
	return c.postJSON(ctx, "/leases/"+url.PathEscape(leaseID)+"/withdraw", nil, nil)
}

// UpdateLease patches a lease (managers/admins). Only keys present in patch are
// sent; a nil value clears the field (e.g. "maxBudget": null removes the limit).
func (c *Client) UpdateLease(ctx context.Context, leaseID string, patch map[string]any) (*Lease, error) {
	var out Lease
	if err := c.patchJSON(ctx, "/leases/"+url.PathEscape(leaseID), patch, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// TeamMember is a user a lease is shared with.
type TeamMember struct {
	LeaseID          string `json:"leaseId"`
	SharedOn         string `json:"sharedOn"`
	SharedByUserID   string `json:"sharedByUserId"`
	TeamMemberUserID string `json:"teamMemberUserId"`
	LoginEnabled     bool   `json:"loginEnabled"`
	UserDetails      *struct {
		UserID      string `json:"userId"`
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
	} `json:"userDetails"`
}

// Label returns the best identifier for a team member.
func (m TeamMember) Label() string {
	if m.UserDetails != nil && m.UserDetails.Email != "" {
		return m.UserDetails.Email
	}
	return m.TeamMemberUserID
}

// LeaseTeamMembers lists the users a lease is shared with.
func (c *Client) LeaseTeamMembers(ctx context.Context, leaseID string) ([]TeamMember, error) {
	var out []TeamMember
	if err := c.getJSON(ctx, "/leases/"+url.PathEscape(leaseID)+"/team-members", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ShareLease grants additional users access to a lease.
func (c *Client) ShareLease(ctx context.Context, leaseID string, userIDs []string) error {
	return c.postJSON(ctx, "/leases/"+url.PathEscape(leaseID)+"/team-members",
		map[string][]string{"userIds": userIDs}, nil)
}

// UnshareLease revokes users' access to a lease.
func (c *Client) UnshareLease(ctx context.Context, leaseID string, userIDs []string) error {
	return c.deleteJSON(ctx, "/leases/"+url.PathEscape(leaseID)+"/team-members",
		map[string][]string{"userIds": userIDs}, nil)
}

// LeaseLogs returns the account setup logs for a lease.
func (c *Client) LeaseLogs(ctx context.Context, leaseID string) (string, error) {
	var out struct {
		Logs string `json:"logs"`
	}
	if err := c.getJSON(ctx, "/leases/"+url.PathEscape(leaseID)+"/logs", &out); err != nil {
		return "", err
	}
	return out.Logs, nil
}

// LeaseSecrets returns the decrypted build-output secrets for a lease.
func (c *Client) LeaseSecrets(ctx context.Context, leaseID string) (map[string]string, error) {
	var out map[string]string
	if err := c.getJSON(ctx, "/leases/"+url.PathEscape(leaseID)+"/secrets", &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListApprovals lists lease requests awaiting (or having had) your review.
// statuses are any of pending, approved, denied, withdrawn; empty means the
// server default.
func (c *Client) ListApprovals(ctx context.Context, statuses []string) ([]Lease, error) {
	q := url.Values{}
	if len(statuses) > 0 {
		q.Set("status", strings.Join(statuses, ","))
	}
	return c.listLeasesPaged(ctx, "/approvals", q)
}

// PublicTemplate is a template any user may request a lease from.
type PublicTemplate struct {
	TemplateID         string  `json:"templateId"`
	Name               string  `json:"name"`
	Description        string  `json:"description"`
	MaxBudget          float64 `json:"maxBudget"`
	MaxDurationInHours float64 `json:"maxDurationInHours"`
	RequiresApproval   bool    `json:"requiresApproval"`
	LeasesCanBeShared  bool    `json:"leasesCanBeShared"`
	Owner              struct {
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
	} `json:"owner"`
}

type listTemplatesResponse struct {
	Result             []PublicTemplate `json:"result"`
	NextPageIdentifier *string          `json:"nextPageIdentifier"`
}

// ListPublicTemplates lists the templates you can request a lease from.
func (c *Client) ListPublicTemplates(ctx context.Context) ([]PublicTemplate, error) {
	var all []PublicTemplate
	next := ""
	for {
		q := url.Values{}
		q.Set("pageSize", "200")
		if next != "" {
			q.Set("pageIdentifier", next)
		}
		var out listTemplatesResponse
		if err := c.getJSON(ctx, "/templates/public?"+q.Encode(), &out); err != nil {
			return nil, err
		}
		all = append(all, out.Result...)
		if out.NextPageIdentifier == nil || *out.NextPageIdentifier == "" {
			return all, nil
		}
		next = *out.NextPageIdentifier
	}
}
