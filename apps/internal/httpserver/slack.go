package httpserver

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/slackbot"
)

// SlackService is what the Slack surface's routes ask of it.
// *slackbot.Service is one.
type SlackService interface {
	Status(ctx context.Context, credentials authclient.Credentials, organizationID string) (slackbot.StatusView, error)
	Install(ctx context.Context, credentials authclient.Credentials, organizationID, returnTo string) (slackbot.InstallView, error)
	Uninstall(ctx context.Context, credentials authclient.Credentials, organizationID string) error
	CompleteInstall(ctx context.Context, callback slackbot.InstallCallback) (slackbot.InstallCompletion, error)
	Channels(ctx context.Context, credentials authclient.Credentials, organizationID string) (slackbot.ChannelsView, error)
	SetHighPriority(ctx context.Context, credentials authclient.Credentials, organizationID string, channelIDs []string) (slackbot.ChannelsView, error)

	LinkStatus(ctx context.Context, credentials authclient.Credentials, organizationID string) (slackbot.LinkView, error)
	StartLink(ctx context.Context, credentials authclient.Credentials, organizationID, returnTo string) (slackbot.LinkStart, error)
	CompleteLink(ctx context.Context, credentials authclient.Credentials, callback slackbot.InstallCallback) (slackbot.LinkCompletion, error)
	Unlink(ctx context.Context, credentials authclient.Credentials, organizationID string) error

	Inbound(ctx context.Context, id string) (slackbot.InboundView, error)
	Profiles(ctx context.Context, organizationID string, userIDs []string) (slackbot.ProfilesView, error)
	Channel(ctx context.Context, organizationID, channelID string) (slackbot.ChannelView, error)
	Post(ctx context.Context, request slackbot.PostRequest) (slackbot.PostResult, error)
	Update(ctx context.Context, request slackbot.UpdateRequest) (slackbot.PostResult, error)
}

// Slack is the Slack surface's part of the API. Without it there is none of
// it: no route below is served, and every path answers as it did before the
// surface existed.
type Slack struct {
	Service SlackService
	// Events and Interactivity are the ingress at Slack's two static URLs:
	// hooks' alias handlers, which verify each delivery before the surface
	// sees it.
	Events, Interactivity http.Handler
}

// SlackPurpose is the purpose of the service tokens the Slack surface's
// internal routes take. It is not InternalPurpose: a service allowed to read
// a deployment endpoint's URL may not post to Slack, and the other way round.
const SlackPurpose = slackbot.Purpose

// routeSlack registers the Slack surface's routes: the ingress, an admin's
// install and channels, and, where services may call apps, the internal
// routes they read inbound records and post through.
func (server *Server) routeSlack(slack *Slack) {
	if slack == nil {
		return
	}
	server.slack = slack.Service
	for _, route := range []struct {
		pattern string
		ingress http.Handler
	}{
		{"POST " + slackbot.EventsPath, slack.Events},
		{"POST " + slackbot.InteractivityPath, slack.Interactivity},
	} {
		ingress := route.ingress
		if ingress == nil {
			// No endpoint exists where no ingress runs: as for hooks', every
			// delivery is unknown.
			ingress = http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writeError(writer, http.StatusNotFound, "not_found", "resource not found")
			})
		}
		server.handleHandler(route.pattern, ingress)
	}
	server.handle("GET "+slackbot.InstallCallbackPath, server.slackInstallCallback)
	server.handle("GET /v1/organizations/{organizationID}/apps/slack/install", server.slackInstall)
	server.handle("GET /v1/organizations/{organizationID}/apps/slack/status", server.slackStatus)
	server.handle("DELETE /v1/organizations/{organizationID}/apps/slack/install", server.slackUninstall)
	server.handle("GET /v1/organizations/{organizationID}/apps/slack/channels", server.slackChannels)
	server.handle("PUT /v1/organizations/{organizationID}/apps/slack/channels", server.slackSetChannels)
	server.handle("GET "+slackbot.LinkCallbackPath, server.slackLinkCallback)
	server.handle("GET /v1/organizations/{organizationID}/apps/slack/link", server.slackLinkStatus)
	server.handle("POST /v1/organizations/{organizationID}/apps/slack/link", server.slackStartLink)
	server.handle("DELETE /v1/organizations/{organizationID}/apps/slack/link", server.slackUnlink)
	if server.services != nil {
		server.handle("GET /v1/internal/apps/slack/inbound/{id}", server.slackInbound)
		server.handle("POST /v1/internal/apps/slack/profiles", server.slackProfiles)
		server.handle("GET /v1/internal/apps/slack/channels/{id}", server.slackChannel)
		server.handle("POST /v1/internal/apps/slack/post", server.slackPost)
		server.handle("POST /v1/internal/apps/slack/update", server.slackUpdate)
	}
}

// slackFailed answers a failed call of the surface with the API's error codes,
// and with what its callers need beyond them: Retry-After on a rate limit, a
// code of their own for Slack refusing, and one for a conversation Slack will
// not describe.
func (server *Server) slackFailed(writer http.ResponseWriter, request *http.Request, err error) {
	var limited interface{ RetryAfterSeconds() int }
	var refused *slackbot.Error
	switch {
	case errors.As(err, &limited):
		writer.Header().Set("Retry-After", strconv.Itoa(limited.RetryAfterSeconds()))
		server.handleError(writer, request, err)
	case errors.Is(err, slackbot.ErrChannelUnreadable):
		writeError(writer, http.StatusUnprocessableEntity, "channel_unreadable", err.Error())
	case errors.As(err, &refused):
		// Slack's error codes are short identifiers, and safe to say.
		server.logger.WarnContext(request.Context(), "slack refused a call", "path", request.URL.Path, "method", refused.Method, "error", refused.Code)
		writeError(writer, http.StatusBadGateway, "slack_error", "slack refused the request: "+refused.Code)
	default:
		server.handleError(writer, request, err)
	}
}

// slackStatus is a separate member-safe read; the admin install endpoint
// creates OAuth state and cannot serve Home's connection-status polling.
func (server *Server) slackStatus(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	view, err := server.slack.Status(request.Context(), credentials, request.PathValue("organizationID"))
	if err != nil {
		server.slackFailed(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

// ---- Admin routes: an admin of the organization, with a session ----

// slackInstall answers an organization's Slack install and the link to
// install it. With return_to, the browser is sent there when the install
// ends.
func (server *Server) slackInstall(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	view, err := server.slack.Install(request.Context(), credentials, request.PathValue("organizationID"), request.URL.Query().Get("return_to"))
	if err != nil {
		server.slackFailed(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) slackUninstall(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if err := server.slack.Uninstall(request.Context(), credentials, request.PathValue("organizationID")); err != nil {
		server.slackFailed(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) slackChannels(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	view, err := server.slack.Channels(request.Context(), credentials, request.PathValue("organizationID"))
	if err != nil {
		server.slackFailed(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

// slackSetChannels makes exactly the channels named the high priority ones.
func (server *Server) slackSetChannels(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	// The list is required, and an empty one unmarks every channel: a body
	// that leaves it out is a mistake, not a request to do that.
	var body struct {
		HighPriority *[]string `json:"high_priority"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	if body.HighPriority == nil {
		writeError(writer, http.StatusBadRequest, "invalid_request", "high_priority is required: the channels to mark, or [] to unmark every one")
		return
	}
	view, err := server.slack.SetHighPriority(request.Context(), credentials, request.PathValue("organizationID"), *body.HighPriority)
	if err != nil {
		server.slackFailed(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

// slackInstallCallback is where Slack sends an admin's browser back after the
// consent page. Like Oryxa's own sign-in callback it answers a browser: to the
// page the install named, saying how it went, or with JSON when it named none.
func (server *Server) slackInstallCallback(writer http.ResponseWriter, request *http.Request) {
	// The code and state in this URL must not follow the browser anywhere.
	writer.Header().Set("Referrer-Policy", "no-referrer")
	if !server.callbacks.Allow(clientIP(request)) {
		server.logger.WarnContext(request.Context(), "apps request rate limited", "path", request.URL.Path)
		writeError(writer, http.StatusTooManyRequests, "rate_limited", "too many requests; retry")
		return
	}
	query := request.URL.Query()
	completion, err := server.slack.CompleteInstall(request.Context(), slackbot.InstallCallback{
		State: query.Get("state"), Code: query.Get("code"), Error: query.Get("error"),
	})
	var failed *slackbot.InstallFailure
	switch {
	case errors.As(err, &failed) && failed.ReturnTo != "":
		sendBackFromSlack(writer, request, failed.ReturnTo, url.Values{"slack": {"error"}, "slack_error": {failed.Reason}})
		return
	case err != nil:
		server.slackFailed(writer, request, err)
		return
	case completion.ReturnTo != "":
		sendBackFromSlack(writer, request, completion.ReturnTo, url.Values{"slack": {"connected"}})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"installed": true, "organization_id": completion.OrganizationID,
		"team": map[string]string{"id": completion.TeamID, "name": completion.TeamName},
	})
}

// ---- A member's own Slack link: any member, with a session ----

// slackLinkStatus answers the caller's own Slack link.
func (server *Server) slackLinkStatus(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	view, err := server.slack.LinkStatus(request.Context(), credentials, request.PathValue("organizationID"))
	if err != nil {
		server.slackFailed(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

// slackStartLink makes the caller's link to Sign in with Slack. With
// return_to, the browser is sent there when the link ends.
func (server *Server) slackStartLink(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	var body struct {
		ReturnTo string `json:"return_to"`
	}
	if request.ContentLength != 0 && !server.decode(writer, request, &body) {
		return
	}
	start, err := server.slack.StartLink(request.Context(), credentials, request.PathValue("organizationID"), body.ReturnTo)
	if err != nil {
		server.slackFailed(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, start)
}

func (server *Server) slackUnlink(writer http.ResponseWriter, request *http.Request) {
	credentials, ok := server.credentials(writer, request)
	if !ok {
		return
	}
	if err := server.slack.Unlink(request.Context(), credentials, request.PathValue("organizationID")); err != nil {
		server.slackFailed(writer, request, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

// slackLinkCallback is where Sign in with Slack sends a member's browser back.
// As for an install, it answers the page the link named, saying how it went,
// or JSON when it named none.
func (server *Server) slackLinkCallback(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Referrer-Policy", "no-referrer")
	if !server.callbacks.Allow(clientIP(request)) {
		server.logger.WarnContext(request.Context(), "apps request rate limited", "path", request.URL.Path)
		writeError(writer, http.StatusTooManyRequests, "rate_limited", "too many requests; retry")
		return
	}
	// The browser's own session, checked against the member who began the
	// link; having none is the service's to refuse, with the page to go back to.
	credentials := authclient.Credentials{Authorization: request.Header.Get("Authorization")}
	if cookie, err := request.Cookie(authclient.AccessCookieName); err == nil && credentials.Authorization == "" {
		credentials.AccessCookie = cookie.Value
	}
	query := request.URL.Query()
	completion, err := server.slack.CompleteLink(request.Context(), credentials, slackbot.InstallCallback{
		State: query.Get("state"), Code: query.Get("code"), Error: query.Get("error"),
	})
	var failed *slackbot.InstallFailure
	switch {
	case errors.As(err, &failed) && failed.ReturnTo != "":
		sendBackFromSlack(writer, request, failed.ReturnTo, url.Values{"slack_link": {"error"}, "slack_link_error": {failed.Reason}})
		return
	case err != nil:
		server.slackFailed(writer, request, err)
		return
	case completion.ReturnTo != "":
		sendBackFromSlack(writer, request, completion.ReturnTo, url.Values{"slack_link": {"linked"}})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"linked": true, "organization_id": completion.OrganizationID, "team_id": completion.TeamID})
}

// sendBackFromSlack redirects the browser to a page the install allowed, with
// exactly the values that say how it went.
func sendBackFromSlack(writer http.ResponseWriter, request *http.Request, returnTo string, values url.Values) {
	target, err := url.Parse(returnTo)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "internal", "internal server error")
		return
	}
	query := target.Query()
	for name, value := range values {
		query[name] = value
	}
	target.RawQuery = query.Encode()
	http.Redirect(writer, request, target.String(), http.StatusSeeOther)
}

// ---- Internal routes: a service with the purpose slack, never a member ----

// slackCaller checks an internal route's service token for the Slack purpose.
func (server *Server) slackCaller(writer http.ResponseWriter, request *http.Request) bool {
	return server.internalCaller(writer, request, SlackPurpose)
}

func (server *Server) slackInbound(writer http.ResponseWriter, request *http.Request) {
	if !server.slackCaller(writer, request) {
		return
	}
	view, err := server.slack.Inbound(request.Context(), request.PathValue("id"))
	if err != nil {
		server.slackFailed(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) slackProfiles(writer http.ResponseWriter, request *http.Request) {
	if !server.slackCaller(writer, request) {
		return
	}
	var body struct {
		OrganizationID string   `json:"organization_id"`
		UserIDs        []string `json:"user_ids"`
	}
	if !server.decode(writer, request, &body) {
		return
	}
	view, err := server.slack.Profiles(request.Context(), body.OrganizationID, body.UserIDs)
	if err != nil {
		server.slackFailed(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

// slackChannel reads a conversation from Slack, now, for the organization
// named in organization_id.
func (server *Server) slackChannel(writer http.ResponseWriter, request *http.Request) {
	if !server.slackCaller(writer, request) {
		return
	}
	view, err := server.slack.Channel(request.Context(), request.URL.Query().Get("organization_id"), request.PathValue("id"))
	if err != nil {
		server.slackFailed(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) slackPost(writer http.ResponseWriter, request *http.Request) {
	if !server.slackCaller(writer, request) {
		return
	}
	var body slackbot.PostRequest
	if !server.decode(writer, request, &body) {
		return
	}
	result, err := server.slack.Post(request.Context(), body)
	if err != nil {
		server.slackFailed(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}

func (server *Server) slackUpdate(writer http.ResponseWriter, request *http.Request) {
	if !server.slackCaller(writer, request) {
		return
	}
	var body slackbot.UpdateRequest
	if !server.decode(writer, request, &body) {
		return
	}
	result, err := server.slack.Update(request.Context(), body)
	if err != nil {
		server.slackFailed(writer, request, err)
		return
	}
	writeJSON(writer, http.StatusOK, result)
}
