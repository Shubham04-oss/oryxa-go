// Package httpserver serves the organization-scoped workspace API.
package httpserver

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/cleantext"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

type Repository interface {
	WithApplyLock(context.Context, string, func() error) error
	VersionNumber(context.Context, string) (int64, error)
	Latest(context.Context, string) (store.Version, error)
	AtVersion(context.Context, string, int64) (store.Version, error)
	Versions(context.Context, string) ([]store.Version, error)
	Save(context.Context, string, string, []byte, []store.ExternalMapping) (store.Version, error)
	Mappings(context.Context, string) ([]store.ExternalMapping, error)
	SavePlan(context.Context, store.ReviewedPlan) error
	ReviewedPlan(context.Context, string, string, string) (store.ReviewedPlan, error)
	SaveAttempt(context.Context, store.ApplyAttempt) error
	LatestAttempt(context.Context, string) (store.ApplyAttempt, error)
}

type Server struct {
	repository Repository
	apiURL     string
	http       *http.Client
	mux        *http.ServeMux
	patterns   []string
	contexts   contextCache
}

func New(repository Repository, apiURL string) (*Server, error) {
	if repository == nil || strings.TrimSpace(apiURL) == "" {
		return nil, fmt.Errorf("workspace API requires a repository and API URL")
	}
	validated, err := runtime.ValidateAPIURL(apiURL)
	if err != nil {
		return nil, fmt.Errorf("workspace API URL is invalid: %w", err)
	}
	server := &Server{
		repository: repository, apiURL: validated,
		http: &http.Client{Timeout: 15 * time.Second, Transport: observability.Transport(nil), CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
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
	server.handle("GET /v1/organizations/{organizationID}/workspace/mappings", server.mappings)
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
	client, err := server.client(actor{Token: parts[1]})
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace API URL is invalid")
		return actor{}, false
	}
	access, err := client.OrganizationAccess(request.Context(), organizationID)
	if err != nil {
		status := http.StatusBadGateway
		var remote *runtime.StatusError
		if errors.As(err, &remote) && (remote.Status == http.StatusUnauthorized || remote.Status == http.StatusForbidden || remote.Status == http.StatusNotFound) {
			status = remote.Status
		}
		writeError(writer, status, "auth did not authorize this organization")
		return actor{}, false
	}
	if access.Organization.ID != organizationID || access.Membership.OrganizationID != organizationID || access.Membership.UserID == "" {
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
	if err := cleantext.Check(body); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
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
	client, err := runtime.NewClient(server.apiURL, actor.Token)
	if err != nil {
		return nil, err
	}
	httpClient := *server.http
	client.HTTP = &httpClient
	return client, nil
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
	baseVersion, err := server.currentVersion(request.Context(), request.PathValue("organizationID"))
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace version could not be read")
		return
	}
	plan, _, err := server.documentPlan(request.Context(), request.PathValue("organizationID"), document, state, false)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	plan, err = server.issuePlan(request.Context(), request.PathValue("organizationID"), access, document, plan, false, baseVersion)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace plan could not be saved")
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
	return spec.ParseStored(latest.Document)
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
	mappings, err := server.repository.Mappings(ctx, organizationID)
	if err != nil {
		return runtime.Plan{}, spec.Document{}, err
	}
	effective, directoryPlan, err := runtime.MergeDirectory(previous, effective, state, mappings, replaceContext)
	if err != nil {
		return runtime.Plan{}, spec.Document{}, err
	}
	effective, policyPlan, err := runtime.MergePolicies(previous, effective, replaceContext)
	if err != nil {
		return runtime.Plan{}, spec.Document{}, err
	}
	return runtime.CombinePlans(servicePlan, contextPlan, directoryPlan, policyPlan), effective, nil
}

func (server *Server) applyDocument(writer http.ResponseWriter, request *http.Request, access actor, document spec.Document, approved runtime.Plan, replaceContext bool) {
	client, err := server.client(access)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace API URL is invalid")
		return
	}
	var remaining runtime.Plan
	var version store.Version
	var attempt *store.ApplyAttempt
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
		issued, err := server.verifyPlan(request.Context(), state.Organization.ID, access, document, plan, approved, replaceContext)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		attempt = &store.ApplyAttempt{ID: rand.Text(), OrganizationID: state.Organization.ID, ActorID: access.ID, Status: "running", Completed: []store.CompletedOperation{}, StartedAt: now, UpdatedAt: now}
		if err := server.repository.SaveAttempt(request.Context(), *attempt); err != nil {
			return err
		}
		base := client.HTTP.Transport
		if base == nil {
			base = http.DefaultTransport
		}
		client.HTTP.Transport = progressTransport{base: base, completed: func(operation *http.Request) error {
			attempt.Completed = append(attempt.Completed, store.CompletedOperation{Method: operation.Method, Path: operation.URL.Path})
			attempt.UpdatedAt = time.Now().UTC()
			return server.repository.SaveAttempt(operation.Context(), *attempt)
		}}
		var current runtime.Snapshot
		remaining, current, err = runtime.ApplyApprovedFromSnapshot(request.Context(), client, document, runtime.RuntimeRemovals(issued), state)
		if err != nil {
			return err
		}
		if len(remaining.Changes) != 0 {
			return nil
		}
		_, contextPlan, err := runtime.MergeContext(effective, effective, current, false)
		if err != nil {
			return err
		}
		remaining = runtime.CombinePlans(remaining, contextPlan)
		encoded, err := effective.YAML()
		if err != nil {
			return err
		}
		mapped, err := server.repository.Mappings(request.Context(), state.Organization.ID)
		if err != nil {
			return err
		}
		resolved, err := runtime.ResolveDirectory(effective, current, mapped)
		if err != nil {
			return err
		}
		version, err = server.repository.Save(request.Context(), state.Organization.ID, access.ID, encoded, resolved)
		return err
	})
	if attempt != nil {
		attempt.Status = "done"
		if err != nil {
			attempt.Status = "partial"
		} else if len(remaining.Changes) > 0 {
			attempt.Status = "incomplete"
		} else if len(remaining.NeedsHuman) > 0 {
			attempt.Status = "needs_human"
		}
		attempt.UpdatedAt = time.Now().UTC()
		progressCtx, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), 5*time.Second)
		progressErr := server.repository.SaveAttempt(progressCtx, *attempt)
		cancel()
		if err == nil {
			err = progressErr
		}
	}
	if err != nil {
		status, body := applyError(err)
		if attempt != nil {
			body["status"] = "partial"
			body["apply_id"] = attempt.ID
			body["completed"] = attempt.Completed
		}
		writeJSON(writer, status, body)
		return
	}
	status := "done"
	if len(remaining.NeedsHuman) > 0 {
		status = "needs_human"
	}
	if len(remaining.Changes) > 0 {
		status = "incomplete"
	}
	writeJSON(writer, http.StatusOK, map[string]any{"step": "apply", "status": status, "remaining": remaining, "version": version.Number, "apply_id": attempt.ID, "completed": attempt.Completed})
}

var errApprovalChanged = errors.New("the removal plan changed; review and confirm it again")

func writeApplyError(writer http.ResponseWriter, err error) {
	status, body := applyError(err)
	writeJSON(writer, status, body)
}

func applyError(err error) (int, map[string]any) {
	status, code, message := http.StatusBadGateway, "upstream_unavailable", "workspace apply could not complete; retry after checking the service"
	var remote *runtime.StatusError
	switch {
	case errors.Is(err, store.ErrApplyBusy):
		status, code, message = http.StatusConflict, "apply_lock", err.Error()
	case errors.Is(err, errApprovalChanged), errors.Is(err, runtime.ErrApprovalChanged), errors.Is(err, errStalePlan):
		status, code, message = http.StatusConflict, "stale_plan", errStalePlan.Error()
	case errors.As(err, &remote) && remote.Status >= 400 && remote.Status < 500:
		status, code, message = remote.Status, "upstream_rejected", remote.Error()
		if remote.Code != "" {
			code = remote.Code
		}
	}
	return status, map[string]any{"code": code, "error": message}
}

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
		previous, parseErr := spec.ParseStored(latest.Document)
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
		document.Directory = previous.Directory
		document.Rules = previous.Rules
		document.Safety = previous.Safety
		document.Tools = previous.Tools
		document.Search = previous.Search
		if previous.Agents != nil && previous.Agents.Personal != nil {
			if document.Agents == nil {
				document.Agents = &spec.Agents{}
			}
			document.Agents.Personal = previous.Agents.Personal
		}
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

func (server *Server) mappings(writer http.ResponseWriter, request *http.Request) {
	_, ok := server.authorize(writer, request)
	if !ok {
		return
	}
	mappings, err := server.repository.Mappings(request.Context(), request.PathValue("organizationID"))
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "external mappings could not be read")
		return
	}
	writeJSON(writer, http.StatusOK, map[string]any{"mappings": mappings})
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
	if err := cleantext.Check(body); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
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
	document, err := spec.ParseStored(version.Document)
	if err != nil || document.Metadata.Organization != access.Slug {
		writeError(writer, http.StatusInternalServerError, "saved workspace file is invalid")
		return
	}
	document = rollbackOrganizationDefaults(document)
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
	if err := cleantext.Check(body); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
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
	document, err := spec.ParseStored(version.Document)
	if err != nil || document.Metadata.Organization != access.Slug {
		writeError(writer, http.StatusInternalServerError, "saved workspace file is invalid")
		return
	}
	document = rollbackOrganizationDefaults(document)
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
	baseVersion, err := server.currentVersion(request.Context(), request.PathValue("organizationID"))
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace version could not be read")
		return
	}
	plan, _, err := server.documentPlan(request.Context(), request.PathValue("organizationID"), document, state, true)
	if err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	plan, err = server.issuePlan(request.Context(), request.PathValue("organizationID"), access, document, plan, true, baseVersion)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace plan could not be saved")
		return
	}
	writeJSON(writer, http.StatusOK, plan)
}

func (server *Server) context(writer http.ResponseWriter, request *http.Request) {
	access, ok := server.authorizeRole(writer, request, false)
	if !ok {
		return
	}
	previous, err := server.contexts.Get(request.Context(), server.repository, request.PathValue("organizationID"))
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace context could not be read")
		return
	}
	client, err := server.client(access)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace API URL is invalid")
		return
	}
	emails, err := client.DirectoryEmails(request.Context(), request.PathValue("organizationID"))
	if err != nil {
		writeError(writer, http.StatusBadGateway, "members could not be read")
		return
	}
	joined := map[string]bool{}
	for _, email := range emails {
		joined[strings.ToLower(email)] = true
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
	var lastApply *store.ApplyAttempt
	attempt, err := server.repository.LatestAttempt(request.Context(), request.PathValue("organizationID"))
	if err == nil {
		lastApply = &attempt
	} else if !errors.Is(err, store.ErrAttemptNotFound) {
		writeError(writer, http.StatusInternalServerError, "workspace apply progress could not be read")
		return
	}
	latest, err := server.repository.Latest(request.Context(), request.PathValue("organizationID"))
	if errors.Is(err, store.ErrVersionNotFound) {
		status := "not_configured"
		if resumeStatus := applyProgressStatus(lastApply); resumeStatus != "" {
			status = resumeStatus
		}
		writeJSON(writer, http.StatusOK, map[string]any{"status": status, "version": 0, "last_apply": lastApply})
		return
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "workspace history could not be read")
		return
	}
	document, err := spec.ParseStored(latest.Document)
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
		problem := err.Error()
		// Status is a read. An admin can inspect a stored owner-only prune
		// configuration without receiving executable removal instructions.
		if access.Role != "owner" {
			readable := document.Clone().Normalize()
			readable.Teams.Prune = false
			if readable.People != nil {
				readable.People.Prune = false
			}
			servicePlan, err = runtime.BuildPlan(readable, state)
		}
		if err != nil {
			servicePlan = runtime.Plan{}
		}
		servicePlan.NeedsHuman = append(servicePlan.NeedsHuman, runtime.HumanStep{Step: "workspace.review", Why: problem})
	}
	_, contextPlan, err := runtime.MergeContext(document, document, state, false)
	if err != nil {
		contextPlan.NeedsHuman = []runtime.HumanStep{{Step: "context.review", Why: err.Error()}}
	}
	mappings, err := server.repository.Mappings(request.Context(), request.PathValue("organizationID"))
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "external mappings could not be read")
		return
	}
	_, directoryPlan, err := runtime.MergeDirectory(document, document, state, mappings, false)
	if err != nil {
		directoryPlan.NeedsHuman = []runtime.HumanStep{{Step: "directory.review", Why: err.Error()}}
	}
	plan := runtime.CombinePlans(servicePlan, contextPlan, directoryPlan)
	status := "done"
	if len(plan.NeedsHuman) > 0 {
		status = "needs_human"
	}
	if len(plan.Changes) > 0 {
		status = "drift"
	}
	if resumeStatus := applyProgressStatus(lastApply); resumeStatus != "" {
		status = resumeStatus
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"status": status, "version": latest.Number, "plan": plan,
		"last_apply": lastApply,
		"members":    len(state.Members), "teams": len(state.Teams),
		"pending_invitations": len(state.Invitations),
	})
}

func applyProgressStatus(attempt *store.ApplyAttempt) string {
	if attempt == nil {
		return ""
	}
	switch attempt.Status {
	case "partial", "incomplete":
		return "needs_human"
	case "running":
		if time.Since(attempt.UpdatedAt) > runtime.ApplyTimeout+time.Minute {
			return "needs_human"
		}
		return "applying"
	}
	return ""
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
