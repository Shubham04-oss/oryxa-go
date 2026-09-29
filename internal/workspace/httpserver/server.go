// Package httpserver serves the organization-scoped workspace API.
package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

type Repository interface {
	WithApplyLock(context.Context, string, func() error) error
	Latest(context.Context, string) (store.Version, error)
	AtVersion(context.Context, string, int64) (store.Version, error)
	Versions(context.Context, string) ([]store.Version, error)
	Save(context.Context, string, string, []byte) (store.Version, error)
}

type Server struct {
	repository Repository
	apiURL     string
	http       *http.Client
	mux        *http.ServeMux
	patterns   []string
}

func New(repository Repository, apiURL string) (*Server, error) {
	if repository == nil || strings.TrimSpace(apiURL) == "" {
		return nil, fmt.Errorf("workspace API requires a repository and API URL")
	}
	server := &Server{
		repository: repository, apiURL: strings.TrimRight(apiURL, "/"),
		http: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		mux:  http.NewServeMux(),
	}
	server.handle("POST /v1/organizations/{organizationID}/workspace:validate", server.validate)
	server.handle("POST /v1/organizations/{organizationID}/workspace:plan", server.plan)
	server.handle("PUT /v1/organizations/{organizationID}/workspace", server.apply)
	server.handle("GET /v1/organizations/{organizationID}/workspace", server.pull)
	server.handle("GET /v1/organizations/{organizationID}/workspace/versions", server.versions)
	server.handle("GET /v1/organizations/{organizationID}/workspace/versions/{version}", server.version)
	server.handle("POST /v1/organizations/{organizationID}/workspace:rollback", server.rollback)
	server.handle("POST /v1/organizations/{organizationID}/workspace:rollback-plan", server.rollbackPlan)
	server.handle("GET /v1/organizations/{organizationID}/workspace/status", server.status)
	server.handle("GET /v1/organizations/{organizationID}/workspace/context", server.context)
	return server, nil
}

func (server *Server) handle(pattern string, handler http.HandlerFunc) {
	server.mux.HandleFunc(pattern, handler)
	server.patterns = append(server.patterns, pattern)
}

func (server *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	server.mux.ServeHTTP(writer, request)
}

func (server *Server) Patterns() []string { return append([]string(nil), server.patterns...) }

type actor struct {
	ID    string
	Role  string
	Token string
	Slug  string
}

func (server *Server) authorize(writer http.ResponseWriter, request *http.Request) (actor, bool) {
	return server.authorizeRole(writer, request, true)
}

func (server *Server) authorizeRole(writer http.ResponseWriter, request *http.Request, admin bool) (actor, bool) {
	parts := strings.Fields(request.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		writeError(writer, http.StatusUnauthorized, "a member bearer token is required")
		return actor{}, false
	}
	organizationID := request.PathValue("organizationID")
	path := "/v1/organizations/" + url.PathEscape(organizationID)
	authRequest, err := http.NewRequestWithContext(request.Context(), http.MethodGet, server.apiURL+path, nil)
	if err != nil {
		writeError(writer, http.StatusBadGateway, "auth could not be reached")
		return actor{}, false
	}
	authRequest.Header.Set("Authorization", "Bearer "+parts[1])
	response, err := server.http.Do(authRequest)
	if err != nil {
		writeError(writer, http.StatusBadGateway, "auth could not be reached")
		return actor{}, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		status := http.StatusBadGateway
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusNotFound {
			status = response.StatusCode
		}
		writeError(writer, status, "auth did not authorize this organization")
		return actor{}, false
	}
	var access struct {
		Organization struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		} `json:"organization"`
		Membership struct {
			OrganizationID string `json:"organization_id"`
			UserID         string `json:"user_id"`
			Role           string `json:"role"`
		} `json:"membership"`
		Agent bool `json:"agent"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&access); err != nil || access.Organization.ID != organizationID || access.Membership.OrganizationID != organizationID || access.Membership.UserID == "" {
		writeError(writer, http.StatusBadGateway, "auth returned an invalid organization")
		return actor{}, false
	}
	if admin && (access.Agent || (access.Membership.Role != "owner" && access.Membership.Role != "admin")) {
		writeError(writer, http.StatusForbidden, "an organization owner or admin is required")
		return actor{}, false
	}
	return actor{ID: access.Membership.UserID, Role: access.Membership.Role, Token: parts[1], Slug: access.Organization.Slug}, true
}

type submitted struct {
	Document string       `json:"document"`
	Approved runtime.Plan `json:"approved_plan"`
}

func decodeSubmitted(writer http.ResponseWriter, request *http.Request, slug string) (submitted, spec.Document, bool) {
	var body submitted
	decoder := json.NewDecoder(io.LimitReader(request.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || strings.TrimSpace(body.Document) == "" {
		writeError(writer, http.StatusBadRequest, "document is required")
		return submitted{}, spec.Document{}, false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		writeError(writer, http.StatusBadRequest, "one request body is required")
		return submitted{}, spec.Document{}, false
	}
	document, err := spec.Parse([]byte(body.Document))
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return submitted{}, spec.Document{}, false
	}
	if document.Metadata.Organization != slug {
		writeError(writer, http.StatusBadRequest, "metadata.organization must match the requested organization")
		return submitted{}, spec.Document{}, false
	}
	return body, document, true
}

func (server *Server) client(actor actor) (*runtime.Client, error) {
	return runtime.NewClient(server.apiURL, actor.Token)
}

func (server *Server) validate(writer http.ResponseWriter, request *http.Request) {
	access, ok := server.authorize(writer, request)
	if !ok {
		return
	}
	_, document, ok := decodeSubmitted(writer, request, access.Slug)
	if !ok {
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"step": "validate", "status": "done", "organization": document.Metadata.Organization})
}

func (server *Server) plan(writer http.ResponseWriter, request *http.Request) {
	access, ok := server.authorize(writer, request)
	if !ok {
		return
	}
	_, document, ok := decodeSubmitted(writer, request, access.Slug)
	if !ok {
		return
	}
	client, err := server.client(access)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace API URL is invalid")
		return
	}
	state, err := client.Snapshot(request.Context(), access.Slug)
	if err != nil {
		writeError(writer, http.StatusBadGateway, err.Error())
		return
	}
	plan, _, err := server.documentPlan(request.Context(), request.PathValue("organizationID"), document, state, false)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(writer, http.StatusOK, plan)
}

func (server *Server) apply(writer http.ResponseWriter, request *http.Request) {
	access, ok := server.authorize(writer, request)
	if !ok {
		return
	}
	body, document, ok := decodeSubmitted(writer, request, access.Slug)
	if !ok {
		return
	}
	server.applyDocument(writer, request, access, document, body.Approved, false)
}

func (server *Server) previous(ctx context.Context, organizationID string) (spec.Document, error) {
	latest, err := server.repository.Latest(ctx, organizationID)
	if errors.Is(err, store.ErrVersionNotFound) {
		return spec.Document{}, nil
	}
	if err != nil {
		return spec.Document{}, err
	}
	return spec.Parse(latest.Document)
}

func (server *Server) documentPlan(ctx context.Context, organizationID string, document spec.Document, state runtime.Snapshot, replaceContext bool) (runtime.Plan, spec.Document, error) {
	previous, err := server.previous(ctx, organizationID)
	if err != nil {
		return runtime.Plan{}, spec.Document{}, err
	}
	servicePlan, err := runtime.BuildPlan(document, state)
	if err != nil {
		return runtime.Plan{}, spec.Document{}, err
	}
	effective, contextPlan, err := runtime.MergeContext(previous, document, state, replaceContext)
	if err != nil {
		return runtime.Plan{}, spec.Document{}, err
	}
	return runtime.CombinePlans(servicePlan, contextPlan), effective, nil
}

func (server *Server) applyDocument(writer http.ResponseWriter, request *http.Request, access actor, document spec.Document, approved runtime.Plan, replaceContext bool) {
	client, err := server.client(access)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace API URL is invalid")
		return
	}
	var remaining runtime.Plan
	var version store.Version
	err = server.repository.WithApplyLock(request.Context(), request.PathValue("organizationID"), func() error {
		state, err := client.Snapshot(request.Context(), access.Slug)
		if err != nil {
			return err
		}
		if state.Organization == nil || state.Organization.ID != request.PathValue("organizationID") {
			return fmt.Errorf("workspace organization is not available")
		}
		plan, effective, err := server.documentPlan(request.Context(), state.Organization.ID, document, state, replaceContext)
		if err != nil {
			return err
		}
		if runtime.RemovalsDiffer(plan, approved) {
			return errApprovalChanged
		}
		remaining, err = runtime.ApplyApproved(request.Context(), client, document, runtime.RuntimeRemovals(approved))
		if err != nil {
			return err
		}
		if len(remaining.Changes) != 0 {
			return nil
		}
		_, contextPlan, err := runtime.MergeContext(effective, effective, state, false)
		if err != nil {
			return err
		}
		remaining = runtime.CombinePlans(remaining, contextPlan)
		encoded, err := effective.YAML()
		if err != nil {
			return err
		}
		version, err = server.repository.Save(request.Context(), state.Organization.ID, access.ID, encoded)
		return err
	})
	if err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, store.ErrApplyBusy) || errors.Is(err, errApprovalChanged) {
			status = http.StatusConflict
		}
		writeError(writer, status, err.Error())
		return
	}
	status := "done"
	if len(remaining.NeedsHuman) > 0 {
		status = "needs_human"
	}
	if len(remaining.Changes) > 0 {
		status = "incomplete"
	}
	writeJSON(writer, http.StatusOK, map[string]any{"step": "apply", "status": status, "remaining": remaining, "version": version.Number})
}

var errApprovalChanged = errors.New("the removal plan changed; review and confirm it again")

func (server *Server) pull(writer http.ResponseWriter, request *http.Request) {
	access, ok := server.authorize(writer, request)
	if !ok {
		return
	}
	client, err := server.client(access)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace API URL is invalid")
		return
	}
	state, err := client.Snapshot(request.Context(), access.Slug)
	if err != nil {
		writeError(writer, http.StatusBadGateway, err.Error())
		return
	}
	document, err := runtime.Pull(state)
	if err != nil {
		writeError(writer, http.StatusBadGateway, err.Error())
		return
	}
	latest, err := server.repository.Latest(request.Context(), request.PathValue("organizationID"))
	if err == nil {
		previous, parseErr := spec.Parse(latest.Document)
		if parseErr != nil {
			writeError(writer, http.StatusInternalServerError, "saved workspace file is invalid")
			return
		}
		document.Preset = previous.Preset
		if previous.People != nil {
			document.People.Invite = previous.People.Invite
			document.People.Prune = previous.People.Prune
		}
		document.Teams.Prune = previous.Teams.Prune
		document.Ownership = previous.Ownership
		document.OrgChart = previous.OrgChart
	} else if !errors.Is(err, store.ErrVersionNotFound) {
		writeError(writer, http.StatusInternalServerError, "workspace history could not be read")
		return
	}
	encoded, err := document.YAML()
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace file could not be encoded")
		return
	}
	writer.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(encoded)
}

func (server *Server) versions(writer http.ResponseWriter, request *http.Request) {
	_, ok := server.authorize(writer, request)
	if !ok {
		return
	}
	versions, err := server.repository.Versions(request.Context(), request.PathValue("organizationID"))
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace history could not be read")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"versions": versions})
}

func (server *Server) version(writer http.ResponseWriter, request *http.Request) {
	_, ok := server.authorize(writer, request)
	if !ok {
		return
	}
	number, err := strconv.ParseInt(request.PathValue("version"), 10, 64)
	if err != nil || number <= 0 {
		writeError(writer, http.StatusBadRequest, "a positive version is required")
		return
	}
	version, err := server.repository.AtVersion(request.Context(), request.PathValue("organizationID"), number)
	if errors.Is(err, store.ErrVersionNotFound) {
		writeError(writer, http.StatusNotFound, "workspace version was not found")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace version could not be read")
		return
	}
	writer.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(version.Document)
}

func (server *Server) rollback(writer http.ResponseWriter, request *http.Request) {
	access, ok := server.authorize(writer, request)
	if !ok {
		return
	}
	var body struct {
		To       int64        `json:"to"`
		Approved runtime.Plan `json:"approved_plan"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body.To <= 0 {
		writeError(writer, http.StatusBadRequest, "a positive to version is required")
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		writeError(writer, http.StatusBadRequest, "one request body is required")
		return
	}
	version, err := server.repository.AtVersion(request.Context(), request.PathValue("organizationID"), body.To)
	if errors.Is(err, store.ErrVersionNotFound) {
		writeError(writer, http.StatusNotFound, "workspace version was not found")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace version could not be read")
		return
	}
	document, err := spec.Parse(version.Document)
	if err != nil || document.Metadata.Organization != access.Slug {
		writeError(writer, http.StatusInternalServerError, "saved workspace file is invalid")
		return
	}
	server.applyDocument(writer, request, access, document, body.Approved, true)
}

func (server *Server) rollbackPlan(writer http.ResponseWriter, request *http.Request) {
	access, ok := server.authorize(writer, request)
	if !ok {
		return
	}
	var body struct {
		To int64 `json:"to"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil || body.To <= 0 {
		writeError(writer, http.StatusBadRequest, "a positive to version is required")
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		writeError(writer, http.StatusBadRequest, "one request body is required")
		return
	}
	version, err := server.repository.AtVersion(request.Context(), request.PathValue("organizationID"), body.To)
	if errors.Is(err, store.ErrVersionNotFound) {
		writeError(writer, http.StatusNotFound, "workspace version was not found")
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace version could not be read")
		return
	}
	document, err := spec.Parse(version.Document)
	if err != nil || document.Metadata.Organization != access.Slug {
		writeError(writer, http.StatusInternalServerError, "saved workspace file is invalid")
		return
	}
	client, err := server.client(access)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace API URL is invalid")
		return
	}
	state, err := client.Snapshot(request.Context(), access.Slug)
	if err != nil {
		writeError(writer, http.StatusBadGateway, err.Error())
		return
	}
	plan, _, err := server.documentPlan(request.Context(), request.PathValue("organizationID"), document, state, true)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(writer, http.StatusOK, plan)
}

func (server *Server) context(writer http.ResponseWriter, request *http.Request) {
	access, ok := server.authorizeRole(writer, request, false)
	if !ok {
		return
	}
	previous, err := server.previous(request.Context(), request.PathValue("organizationID"))
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace context could not be read")
		return
	}
	path := "/v1/organizations/" + url.PathEscape(request.PathValue("organizationID")) + "/directory"
	membersRequest, err := http.NewRequestWithContext(request.Context(), http.MethodGet, server.apiURL+path, nil)
	if err != nil {
		writeError(writer, http.StatusBadGateway, "members could not be reached")
		return
	}
	membersRequest.Header.Set("Authorization", "Bearer "+access.Token)
	response, err := server.http.Do(membersRequest)
	if err != nil {
		writeError(writer, http.StatusBadGateway, "members could not be reached")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		writeError(writer, http.StatusBadGateway, "members could not be read")
		return
	}
	var members struct {
		Members []struct {
			Email string `json:"email"`
		} `json:"members"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&members); err != nil {
		writeError(writer, http.StatusBadGateway, "members response was invalid")
		return
	}
	joined := map[string]bool{}
	for _, member := range members.Members {
		joined[strings.ToLower(member.Email)] = true
	}
	owners := make([]spec.OwnerHint, 0)
	for _, hint := range previous.Ownership.Items {
		if joined[hint.Owner] {
			owners = append(owners, hint)
		}
	}
	chart := spec.OrgChart{Relationships: []spec.Relationship{}, Authority: []string{}, Executives: []string{}}
	if previous.OrgChart != nil {
		chart.Authority = previous.OrgChart.Authority
		for _, relation := range previous.OrgChart.Relationships {
			if joined[relation.From] && joined[relation.To] {
				chart.Relationships = append(chart.Relationships, relation)
			}
		}
		for _, email := range previous.OrgChart.Executives {
			if joined[email] {
				chart.Executives = append(chart.Executives, email)
			}
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ownership": owners, "orgChart": chart})
}

func (server *Server) status(writer http.ResponseWriter, request *http.Request) {
	access, ok := server.authorize(writer, request)
	if !ok {
		return
	}
	latest, err := server.repository.Latest(request.Context(), request.PathValue("organizationID"))
	if errors.Is(err, store.ErrVersionNotFound) {
		writeJSON(writer, http.StatusOK, map[string]any{"status": "not_configured", "version": 0})
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace history could not be read")
		return
	}
	document, err := spec.Parse(latest.Document)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "saved workspace file is invalid")
		return
	}
	client, err := server.client(access)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace API URL is invalid")
		return
	}
	state, err := client.Snapshot(request.Context(), access.Slug)
	if err != nil {
		writeError(writer, http.StatusBadGateway, err.Error())
		return
	}
	servicePlan, err := runtime.BuildPlan(document, state)
	if err != nil {
		writeError(writer, http.StatusBadGateway, err.Error())
		return
	}
	_, contextPlan, err := runtime.MergeContext(document, document, state, false)
	if err != nil {
		contextPlan.NeedsHuman = []runtime.HumanStep{{Step: "context.review", Why: err.Error()}}
	}
	plan := runtime.CombinePlans(servicePlan, contextPlan)
	status := "done"
	if len(plan.NeedsHuman) > 0 {
		status = "needs_human"
	}
	if len(plan.Changes) > 0 {
		status = "drift"
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"status": status, "version": latest.Number, "plan": plan,
		"members": len(state.Members), "teams": len(state.Teams),
		"pending_invitations": len(state.Invitations),
	})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}
