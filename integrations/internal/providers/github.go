package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github"
	"github.com/Shubham04-oss/oryxa-go/internal/providerapi/github/repos"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

var (
	githubRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})/[A-Za-z0-9._-]{1,100}$`)
	githubEventPattern      = regexp.MustCompile(`^[a-z_]{1,64}$`)
)

// githubObjects is, for an event, the payload field holding what the event is
// about. An event not listed is about its repository.
var githubObjects = map[string]string{
	"check_run":                   "check_run",
	"check_suite":                 "check_suite",
	"commit_comment":              "comment",
	"deployment":                  "deployment",
	"deployment_status":           "deployment_status",
	"discussion":                  "discussion",
	"discussion_comment":          "comment",
	"issue_comment":               "comment",
	"issues":                      "issue",
	"label":                       "label",
	"milestone":                   "milestone",
	"pull_request":                "pull_request",
	"pull_request_review":         "review",
	"pull_request_review_comment": "comment",
	"pull_request_review_thread":  "thread",
	"release":                     "release",
	"workflow_job":                "workflow_job",
	"workflow_run":                "workflow_run",
}

// GitHub delivers a repository's events to a webhook integrations creates on
// the repository, signed with the subscription's secret. Creating one needs
// the repo or write:repo_hook scope, or the Webhooks repository permission;
// removing one needs repo or admin:repo_hook.
func GitHub() Provider {
	return githubProvider(github.Server)
}

func githubProvider(apiURL string) Provider {
	return Provider{
		Name:     "github",
		Delivery: DeliveryWebhook,
		Target:   githubTarget,
		Register: func(ctx context.Context, client *http.Client, request RegisterRequest) (Registration, error) {
			return githubRegister(ctx, client, apiURL, request)
		},
		Unregister: func(ctx context.Context, client *http.Client, request UnregisterRequest) error {
			return githubUnregister(ctx, client, apiURL, request)
		},
		Receive: githubReceive,
		TargetFields: []TargetField{
			{Name: "repository", Description: "The repository to watch, as owner/name.", Required: true, Example: "acme/api"},
		},
		Events: EventsDescription{
			Names:   []string{"issues", "issue_comment", "pull_request", "pull_request_review", "pull_request_review_comment", "push", "release", "workflow_run"},
			Open:    true,
			Default: "every event the repository sends",
		},
		Actions: githubActions(apiURL),
		Skill:   skill("github"),
	}
}

// githubTarget takes a repository, as owner/name, and GitHub event names; no
// events, or *, asks for all of them.
func githubTarget(target map[string]string, events []string) (map[string]string, []string, error) {
	repository := target["repository"]
	if len(target) != 1 || !githubRepositoryPattern.MatchString(repository) {
		return nil, nil, fmt.Errorf("a GitHub subscription's target is {\"repository\": \"owner/name\"}: %w", domain.ErrInvalid)
	}
	for _, event := range events {
		if event == "*" && len(events) > 1 {
			return nil, nil, fmt.Errorf("event * asks for every event, so it stands alone: %w", domain.ErrInvalid)
		}
		if event != "*" && !githubEventPattern.MatchString(event) {
			return nil, nil, fmt.Errorf("%q is not a GitHub event name: %w", event, domain.ErrInvalid)
		}
	}
	if slices.Equal(events, []string{"*"}) {
		events = []string{}
	}
	return map[string]string{"repository": repository}, events, nil
}

func githubClient(client *http.Client, apiURL string, credential domain.Credential) (*repos.ClientWithResponses, error) {
	return repos.NewClientWithResponses(apiURL, repos.WithHTTPClient(client),
		repos.WithRequestEditorFn(github.Versioned),
		repos.WithRequestEditorFn(func(_ context.Context, request *http.Request) error {
			Authorize(request, credential)
			return nil
		}))
}

func githubRegister(ctx context.Context, client *http.Client, apiURL string, request RegisterRequest) (Registration, error) {
	api, err := githubClient(client, apiURL, request.Credential)
	if err != nil {
		return Registration{}, fmt.Errorf("%w: GitHub client could not be built", domain.ErrProviderUnavailable)
	}
	owner, name, _ := strings.Cut(request.Target["repository"], "/")
	events := request.Events
	if len(events) == 0 {
		events = []string{"*"}
	}
	var insecure repos.WebhookConfigInsecureSsl
	if err := insecure.FromWebhookConfigInsecureSsl0("0"); err != nil {
		return Registration{}, fmt.Errorf("encode webhook settings: %w", err)
	}
	hookName, active, contentType := "web", true, "json"
	response, err := api.CreateWebhookWithResponse(ctx, owner, name, repos.CreateWebhookJSONRequestBody{
		Name: &hookName, Active: &active, Events: &events,
		Config: &repos.CreateWebhookJSONBody_Config{
			URL: &request.CallbackURL, ContentType: &contentType, Secret: &request.Secret, InsecureSsl: &insecure,
		},
	})
	if err != nil {
		return Registration{}, fmt.Errorf("%w: GitHub request failed", domain.ErrProviderUnavailable)
	}
	if err := githubStatusError(response.HTTPResponse); err != nil {
		return Registration{}, err
	}
	if response.JSON201 == nil || response.JSON201.ID <= 0 {
		return Registration{}, fmt.Errorf("%w: GitHub answered without a hook", domain.ErrProviderUnavailable)
	}
	return Registration{ID: strconv.Itoa(response.JSON201.ID)}, nil
}

func githubUnregister(ctx context.Context, client *http.Client, apiURL string, request UnregisterRequest) error {
	hookID, err := strconv.Atoi(request.RegistrationID)
	if err != nil || hookID <= 0 {
		return fmt.Errorf("%q is not a GitHub hook ID: %w", request.RegistrationID, domain.ErrInvalid)
	}
	api, err := githubClient(client, apiURL, request.Credential)
	if err != nil {
		return fmt.Errorf("%w: GitHub client could not be built", domain.ErrProviderUnavailable)
	}
	owner, name, _ := strings.Cut(request.Target["repository"], "/")
	response, err := api.DeleteWebhookWithResponse(ctx, owner, name, hookID)
	if err != nil {
		return fmt.Errorf("%w: GitHub request failed", domain.ErrProviderUnavailable)
	}
	if response.HTTPResponse.StatusCode == http.StatusNotFound {
		return nil
	}
	return githubStatusError(response.HTTPResponse)
}

// githubStatusError is StatusError, except that GitHub answers a spent rate
// limit with 403 or 429, which is worth retrying.
func githubStatusError(response *http.Response) error {
	if response.StatusCode == http.StatusForbidden && response.Header.Get("X-RateLimit-Remaining") == "0" {
		return fmt.Errorf("%w: GitHub rate limit", domain.ErrProviderUnavailable)
	}
	return StatusError(response.StatusCode)
}

// githubReceive verifies a delivery's X-Hub-Signature-256 and reads its event.
// A ping, sent when a webhook is created, is acknowledged and not published.
func githubReceive(delivery Delivered, secret []byte, _ time.Time) (Reception, error) {
	signature, ok := strings.CutPrefix(delivery.Header.Get("X-Hub-Signature-256"), "sha256=")
	if !ok || !ValidHMACSHA256(secret, delivery.Body, signature) {
		return Reception{}, fmt.Errorf("%w: GitHub signature", ErrUnverified)
	}
	event := delivery.Header.Get("X-GitHub-Event")
	if !githubEventPattern.MatchString(event) {
		return Reception{}, fmt.Errorf("%w: GitHub event %q", domain.ErrInvalid, event)
	}
	if event == "ping" {
		return Reception{}, nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(delivery.Body, &payload); err != nil {
		return Reception{}, fmt.Errorf("%w: GitHub payload is not a JSON object", domain.ErrInvalid)
	}
	name := event
	var action string
	if json.Unmarshal(payload["action"], &action) == nil && action != "" {
		name += "." + action
	}
	deliveryID := delivery.Header.Get("X-GitHub-Delivery")
	key := deliveryID
	if key == "" {
		key = Digest([]byte(event), delivery.Body)
	}
	return Reception{Events: []Event{{
		Name:       name,
		Type:       event,
		DeliveryID: deliveryID,
		Key:        key,
		Resource:   githubResource(event, delivery.Body, payload),
		Actor:      githubActor(payload["sender"]),
		Payload:    delivery.Body,
	}}}, nil
}

type githubObject struct {
	ID       json.Number `json:"id"`
	Number   json.Number `json:"number"`
	Title    string      `json:"title"`
	Name     string      `json:"name"`
	TagName  string      `json:"tag_name"`
	FullName string      `json:"full_name"`
	HTMLURL  string      `json:"html_url"`
}

// githubResource is what an event is about: the object its payload names for
// it, a push's ref, a branch or tag created or deleted, or else its
// repository.
func githubResource(event string, body []byte, payload map[string]json.RawMessage) integrationsv1.Resource {
	var parent githubObject
	for _, field := range []string{"issue", "pull_request", "discussion"} {
		if json.Unmarshal(payload[field], &parent) == nil && parent.Title != "" {
			break
		}
	}
	switch event {
	case "push":
		var push struct {
			Ref     string `json:"ref"`
			After   string `json:"after"`
			Compare string `json:"compare"`
		}
		_ = json.Unmarshal(body, &push)
		return integrationsv1.Resource{Type: "push", ID: push.After, Name: push.Ref, URL: push.Compare}
	case "create", "delete":
		var ref struct {
			Ref     string `json:"ref"`
			RefType string `json:"ref_type"`
		}
		_ = json.Unmarshal(body, &ref)
		return integrationsv1.Resource{Type: firstNonEmpty(ref.RefType, "ref"), ID: ref.Ref, Name: ref.Ref}
	}
	if field, ok := githubObjects[event]; ok {
		var object githubObject
		if json.Unmarshal(payload[field], &object) == nil && object.ID != "" {
			return integrationsv1.Resource{
				Type: field, ID: object.ID.String(),
				Name: firstNonEmpty(object.Title, object.Name, object.TagName, parent.Title), URL: object.HTMLURL,
			}
		}
	}
	var repository githubObject
	if json.Unmarshal(payload["repository"], &repository) == nil && repository.ID != "" {
		return integrationsv1.Resource{Type: "repository", ID: repository.ID.String(), Name: repository.FullName, URL: repository.HTMLURL}
	}
	return integrationsv1.Resource{Type: event}
}

func githubActor(raw json.RawMessage) *integrationsv1.Actor {
	var sender struct {
		ID    json.Number `json:"id"`
		Login string      `json:"login"`
	}
	if json.Unmarshal(raw, &sender) != nil || sender.ID == "" {
		return nil
	}
	return &integrationsv1.Actor{ID: sender.ID.String(), Name: sender.Login}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
