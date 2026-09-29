package runtime

import (
	"context"
	"net/http"
	"net/url"
)

type OrganizationAccess struct {
	Organization Organization `json:"organization"`
	Membership   struct {
		OrganizationID string `json:"organization_id"`
		UserID         string `json:"user_id"`
		Role           string `json:"role"`
	} `json:"membership"`
	Agent bool `json:"agent"`
}

func (client *Client) OrganizationAccess(ctx context.Context, organizationID string) (OrganizationAccess, error) {
	var access OrganizationAccess
	err := client.request(ctx, http.MethodGet, "/v1/organizations/"+url.PathEscape(organizationID), nil, &access)
	return access, err
}

func (client *Client) DirectoryEmails(ctx context.Context, organizationID string) ([]string, error) {
	var directory struct {
		Members []struct {
			Email string `json:"email"`
		} `json:"members"`
	}
	if err := client.request(ctx, http.MethodGet, "/v1/organizations/"+url.PathEscape(organizationID)+"/directory", nil, &directory); err != nil {
		return nil, err
	}
	emails := make([]string, 0, len(directory.Members))
	for _, member := range directory.Members {
		emails = append(emails, member.Email)
	}
	return emails, nil
}
