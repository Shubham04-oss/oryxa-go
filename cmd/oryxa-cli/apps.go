package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
)

var appToolkitPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// connectApp starts a member's own connection. The provider still needs the
// member's browser consent; the CLI follows the saved connection until the
// callback has made it active, and returns a resumable connection ID.
func connectApp(ctx context.Context, toolkit string, opts options, stdout, stderr io.Writer, getenv func(string) string) int {
	if !appToolkitPattern.MatchString(toolkit) {
		return reportInputError(fmt.Errorf("connect-app requires a lowercase toolkit slug"), stderr)
	}
	if opts.org == "" {
		return reportInputError(fmt.Errorf("connect-app requires --org or ORYXA_ORG"), stderr)
	}
	client, err := signedInClient(ctx, opts, stdout, stderr, getenv)
	if err != nil {
		return reportError(err, stderr)
	}
	organization, err := client.OrganizationBySlug(ctx, opts.org)
	if err != nil {
		return reportError(err, stderr)
	}
	offer, err := client.AppOffer(ctx, organization.ID)
	if err != nil {
		return reportError(err, stderr)
	}
	if offer != nil && !slices.Contains(*offer, toolkit) {
		return reportError(fmt.Errorf("%s is not offered by this organization", toolkit), stderr)
	}
	connections, err := client.AppConnections(ctx, organization.ID, toolkit)
	if err != nil {
		return reportError(err, stderr)
	}
	for _, connection := range connections {
		if connection.Toolkit != toolkit {
			continue
		}
		if connection.Status == "active" {
			return report(stdout, opts.json, appConnectionResult("done", toolkit, connection, "", "already connected"))
		}
		if connection.Status == "pending" {
			return appNeedsBrowser(stdout, opts.json, toolkit, connection, pendingAppURL(opts.api, organization.ID, connection.ID))
		}
	}
	connection, redirectURL, err := client.StartAppConnection(ctx, organization.ID, toolkit)
	if err != nil {
		return reportError(err, stderr)
	}
	if connection.ID == "" || connection.Toolkit != toolkit || !validAppRedirect(redirectURL) {
		return reportError(fmt.Errorf("app connection API returned an invalid browser step"), stderr)
	}
	savePendingAppURL(opts.api, organization.ID, connection.ID, redirectURL)
	if opts.noBrowser || opts.noInput {
		return appNeedsBrowser(stdout, opts.json, toolkit, connection, redirectURL)
	}
	if err := openBrowser(redirectURL); err != nil {
		fmt.Fprintln(stderr, "could not open the provider page:", err)
		return appNeedsBrowser(stdout, opts.json, toolkit, connection, redirectURL)
	}
	if code := report(stdout, opts.json, appConnectionResult("needs_human", toolkit, connection, redirectURL, "finish connecting in the browser")); code != 0 {
		return code
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-waitCtx.Done():
			if ctx.Err() != nil {
				return reportError(ctx.Err(), stderr)
			}
			return appNeedsBrowser(stdout, opts.json, toolkit, connection, redirectURL)
		case <-ticker.C:
			updated, err := client.AppConnection(waitCtx, organization.ID, connection.ID)
			if err != nil {
				return reportError(err, stderr)
			}
			switch updated.Status {
			case "active":
				return report(stdout, opts.json, appConnectionResult("done", toolkit, updated, "", "connected"))
			case "failed", "expired", "removed":
				if code := report(stdout, opts.json, appConnectionResult("failed", toolkit, updated, "", updated.StatusReason)); code != 0 {
					return code
				}
				return 1
			}
		}
	}
}

func appNeedsBrowser(stdout io.Writer, asJSON bool, toolkit string, connection runtime.AppConnection, redirectURL string) int {
	if code := report(stdout, asJSON, appConnectionResult("needs_human", toolkit, connection, redirectURL, "open the provider page and finish connecting")); code != 0 {
		return code
	}
	return 2
}

func appConnectionResult(status, toolkit string, connection runtime.AppConnection, redirectURL, detail string) map[string]any {
	result := map[string]any{
		"step": "apps.connect", "status": status, "toolkit": toolkit,
		"connection_id": connection.ID, "connection_status": connection.Status,
	}
	if detail != "" {
		result["detail"] = detail
	}
	if redirectURL != "" {
		result["next"] = map[string]string{"action": "open_url", "url": redirectURL}
	}
	return result
}

func validAppRedirect(target string) bool {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	return parsed.Scheme == "https" || parsed.Scheme == "http" &&
		(strings.EqualFold(parsed.Hostname(), "localhost") || parsed.Hostname() == "127.0.0.1")
}

func pendingAppAccount(api, organization, connection string) string {
	return sessionAccount(api + "/" + organization + "/" + connection)
}
func savePendingAppURL(api, organization, connection, target string) {
	encoded, _ := json.Marshal(struct {
		URL       string
		ExpiresAt time.Time
	}{target, time.Now().Add(30 * time.Minute)})
	_ = sessionStore.Set(sessionService+".connections", pendingAppAccount(api, organization, connection), string(encoded))
}
func pendingAppURL(api, organization, connection string) string {
	account := pendingAppAccount(api, organization, connection)
	encoded, err := sessionStore.Get(sessionService+".connections", account)
	if err != nil {
		return ""
	}
	var pending struct {
		URL       string
		ExpiresAt time.Time
	}
	if json.Unmarshal([]byte(encoded), &pending) != nil || time.Now().After(pending.ExpiresAt) || !validAppRedirect(pending.URL) {
		_ = sessionStore.Delete(sessionService+".connections", account)
		return ""
	}
	return pending.URL
}
