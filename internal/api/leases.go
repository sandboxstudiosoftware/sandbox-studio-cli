package api

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
)

// Lease is a subset of the Sandbox Studio lease shape — the fields the CLI
// currently renders. Unknown fields are ignored by the JSON decoder.
//
// The API nests the template and owner under objects; flat userEmail/userId/
// templateName are null on this endpoint, so we read the nested shapes and expose
// convenience accessors.
type Lease struct {
	LeaseID      string  `json:"leaseId"`
	Status       string  `json:"status"`
	AWSAccountID string  `json:"awsAccountId"`
	MaxBudget    float64 `json:"maxBudget"`
	TotalCost    float64 `json:"totalCostAccrued"`

	// Flat fields (may be null depending on endpoint).
	UserID       string `json:"userId"`
	UserEmail    string `json:"userEmail"`
	TemplateName string `json:"templateName"`

	// Nested objects (the populated shape on GET /leases).
	Template struct {
		TemplateID string `json:"templateId"`
		Name       string `json:"name"`
	} `json:"template"`
	Owner struct {
		UserID      string `json:"userId"`
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
	} `json:"owner"`
}

// TemplateLabel returns the best available template name.
func (l Lease) TemplateLabel() string {
	if l.Template.Name != "" {
		return l.Template.Name
	}
	return l.TemplateName
}

// OwnerLabel returns the best available owner identifier (email, then id).
func (l Lease) OwnerLabel() string {
	switch {
	case l.Owner.Email != "":
		return l.Owner.Email
	case l.UserEmail != "":
		return l.UserEmail
	case l.Owner.UserID != "":
		return l.Owner.UserID
	default:
		return l.UserID
	}
}

// listLeasesResponse mirrors GET /leases.
type listLeasesResponse struct {
	Result             []Lease `json:"result"`
	NextPageIdentifier *string `json:"nextPageIdentifier"`
}

// ListLeases returns leases filtered by the given statuses. Pass nil/empty for no
// status filter (all leases). Multiple statuses are sent comma-separated, matching
// the API's ListLeasesApiRequest.
func (c *Client) ListLeases(ctx context.Context, statuses []string) ([]Lease, error) {
	return c.ListLeasesWith(ctx, ListLeasesOptions{Statuses: statuses})
}

// ListLeasesOptions are the optional filters for GET /leases.
type ListLeasesOptions struct {
	Statuses  []string
	UserID    string // restrict to a user (managers/admins)
	StartDate string // ISO 8601; only leases started on/after this date
}

// ListLeasesWith lists leases with the given filters, following pagination until
// every page has been read.
func (c *Client) ListLeasesWith(ctx context.Context, opts ListLeasesOptions) ([]Lease, error) {
	q := url.Values{}
	if len(opts.Statuses) > 0 {
		q.Set("status", strings.Join(opts.Statuses, ","))
	}
	if opts.UserID != "" {
		q.Set("userId", opts.UserID)
	}
	if opts.StartDate != "" {
		q.Set("startDate", opts.StartDate)
	}
	return c.listLeasesPaged(ctx, "/leases", q)
}

// listLeasesPaged reads every page of a paginated lease list endpoint.
func (c *Client) listLeasesPaged(ctx context.Context, path string, q url.Values) ([]Lease, error) {
	var all []Lease
	next := ""
	for {
		page := url.Values{}
		for k, v := range q {
			page[k] = v
		}
		page.Set("pageSize", "200")
		if next != "" {
			page.Set("pageIdentifier", next)
		}
		var out listLeasesResponse
		if err := c.getJSON(ctx, path+"?"+page.Encode(), &out); err != nil {
			return nil, err
		}
		all = append(all, out.Result...)
		if out.NextPageIdentifier == nil || *out.NextPageIdentifier == "" {
			return all, nil
		}
		next = *out.NextPageIdentifier
	}
}

// GetLease returns a single lease by ID.
func (c *Client) GetLease(ctx context.Context, id string) (*Lease, error) {
	var out Lease
	if err := c.getJSON(ctx, "/leases/"+id, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AccountCredentials are temporary AWS credentials for a sandbox account.
type AccountCredentials struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken"`
	Expiration      string `json:"expiration"`
}

// LeaseAccess is the response of POST /leases/{id}/access.
//
// Credentials last the lease template's session duration (up to 12h) for every
// caller; with role ADMIN (managers/admins only) they last at most 1h.
type LeaseAccess struct {
	Type        string              `json:"type"` // "FEDERATION" | "PORTAL"
	ConsoleURL  string              `json:"consoleUrl"`
	PortalURL   string              `json:"portalUrl"`
	Credentials *AccountCredentials `json:"credentials"`
}

// Access roles accepted by POST /leases/{id}/access.
const (
	AccessRoleUser  = "USER"
	AccessRoleAdmin = "ADMIN"
)

// Error codes returned by POST /leases/{id}/access.
const (
	// CodeFederatedAccessProvisioning (503): the first access to a sandbox role
	// after a backend upgrade is still propagating in IAM. Safe to retry.
	CodeFederatedAccessProvisioning = "FEDERATED_ACCESS_PROVISIONING"
	// CodeLeaseNotActiveForAccess (409): the lease is not active.
	CodeLeaseNotActiveForAccess = "LEASE_NOT_ACTIVE_FOR_ACCESS"
)

// AccessRetries is how many times GetLeaseAccess retries a 503
// FEDERATED_ACCESS_PROVISIONING response, AccessRetryDelay apart. Variables so
// tests can shorten the delay.
var (
	AccessRetries    = 3
	AccessRetryDelay = 3 * time.Second
)

// GetLeaseAccess requests console/CLI access for a lease. role is "USER" or
// "ADMIN"; empty means "USER".
//
// The call is idempotent, so a 503 FEDERATED_ACCESS_PROVISIONING (IAM still
// propagating) is retried up to AccessRetries times before the error is returned.
func (c *Client) GetLeaseAccess(ctx context.Context, leaseID, role string) (*LeaseAccess, error) {
	if role == "" {
		role = AccessRoleUser
	}
	path := "/leases/" + url.PathEscape(leaseID) + "/access"
	for attempt := 0; ; attempt++ {
		var out LeaseAccess
		err := c.postJSON(ctx, path, map[string]string{"role": role}, &out)
		if err == nil {
			return &out, nil
		}
		if attempt >= AccessRetries || !IsProvisioning(err) {
			return nil, err
		}
		select {
		case <-time.After(AccessRetryDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// IsProvisioning reports whether err is the retryable 503
// FEDERATED_ACCESS_PROVISIONING response.
func IsProvisioning(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.StatusCode == 503 && ae.Code == CodeFederatedAccessProvisioning
}
