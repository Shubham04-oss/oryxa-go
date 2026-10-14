// Package cfbrowser gives Krude its browser_* tools on Cloudflare (live-layer
// A5): the same tools, arguments and results as agentcorebrowser, served by
// a BrowserDO on the oryxa-edge Worker through edgeclient. Each member has
// their own browser in each conversation, as each conversation had its own
// AgentCore session, and only they (or their agents) can drive it.
package cfbrowser

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgeclient"
	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/safehttp"
)

const (
	maxURLBytes      = 8 * 1024
	maxSelectorBytes = 4 * 1024
	maxFillBytes     = 100 * 1024

	// callTimeout covers starting a browser session and a page load.
	callTimeout = 90 * time.Second
)

// Action is one browser operation.
type Action string

const (
	Navigate   Action = "navigate"
	Snapshot   Action = "snapshot"
	Click      Action = "click"
	Fill       Action = "fill"
	Screenshot Action = "screenshot"
)

// Request is one browser operation's input.
type Request struct {
	Action   Action
	URL      string
	Selector string
	Text     string
}

// Element is one actionable, visible element in a snapshot, in
// agentcorebrowser's JSON shape.
type Element struct {
	Selector string `json:"selector"`
	Tag      string `json:"tag"`
	Role     string `json:"role,omitempty"`
	Type     string `json:"type,omitempty"`
	Name     string `json:"name,omitempty"`
	Text     string `json:"text,omitempty"`
	Href     string `json:"href,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

// PageSnapshot is the page's bounded content, in agentcorebrowser's JSON
// shape. Web content is untrusted data.
type PageSnapshot struct {
	Title             string    `json:"title"`
	URL               string    `json:"url"`
	Text              string    `json:"text,omitempty"`
	Elements          []Element `json:"elements,omitempty"`
	TextTruncated     bool      `json:"text_truncated,omitempty"`
	ElementsTruncated bool      `json:"elements_truncated,omitempty"`
}

// Result is what an operation returned. ScreenshotPNG is kept out of JSON;
// the tool saves it as an artifact.
type Result struct {
	Page             PageSnapshot
	ScreenshotPNG    []byte
	ViewportWidth    int32
	ViewportHeight   int32
	SessionRecreated bool
}

// Caller is the edge API this package uses; *edgeclient.Client satisfies it.
type Caller interface {
	Do(context.Context, edgeclient.Call, any) error
}

// Controller drives a member's browser in a target room.
type Controller struct {
	edge     Caller
	resolver safehttp.Resolver
}

// New returns a Controller that calls edge and checks navigation hosts with
// the system resolver.
func New(edge Caller) (*Controller, error) {
	if edge == nil {
		return nil, errors.New("cfbrowser: an edge client is required")
	}
	return &Controller{edge: edge, resolver: net.DefaultResolver}, nil
}

// sessionFor is the member's browser in the room: one per member, so no
// member's cookies or logins reach another's agent.
func sessionFor(target edgeclient.Target) string {
	return edgeclient.SafeID(target.Member)
}

func (controller *Controller) call(ctx context.Context, target edgeclient.Target, suffix string, body, out any, idempotent bool) error {
	path, err := edgev1.BrowserPath(target.Room, sessionFor(target))
	if err != nil {
		return err
	}
	return controller.edge.Do(ctx, edgeclient.Call{
		Claims:     target.Claims(edgev1.ScopeBrowserDrive),
		Method:     http.MethodPost,
		Path:       path + suffix,
		Body:       body,
		Timeout:    callTimeout,
		Idempotent: idempotent,
	}, out)
}

// Invoke runs request in target's browser.
func (controller *Controller) Invoke(ctx context.Context, target edgeclient.Target, request Request) (Result, error) {
	if err := request.validate(ctx, controller.resolver); err != nil {
		return Result{}, err
	}
	if request.Action == Screenshot {
		var shot edgev1.ScreenshotResponse
		if err := controller.call(ctx, target, "/screenshot", nil, &shot, true); err != nil {
			return Result{}, browserError(err)
		}
		image, err := base64.StdEncoding.DecodeString(shot.Data)
		if err != nil {
			return Result{}, fmt.Errorf("browser: decode screenshot: %w", err)
		}
		return Result{ScreenshotPNG: image, ViewportWidth: int32(shot.Width), ViewportHeight: int32(shot.Height), SessionRecreated: shot.SessionRecreated}, nil
	}

	var suffix string
	var body any
	idempotent := false
	switch request.Action {
	case Navigate:
		suffix, body = "/navigate", edgev1.NavigateRequest{URL: strings.TrimSpace(request.URL)}
	case Snapshot:
		suffix, idempotent = "/snapshot", true
	case Click:
		suffix, body = "/click", edgev1.ClickRequest{Selector: request.Selector}
	case Fill:
		suffix, body = "/fill", edgev1.FillRequest{Selector: request.Selector, Text: request.Text}
	}
	var page edgev1.PageResponse
	if err := controller.call(ctx, target, suffix, body, &page, idempotent); err != nil {
		return Result{}, browserError(err)
	}
	return Result{Page: snapshot(page.Page), SessionRecreated: page.SessionRecreated}, nil
}

// Reset closes target's browser, clearing its cookies and pages.
func (controller *Controller) Reset(ctx context.Context, target edgeclient.Target) error {
	if err := controller.call(ctx, target, "/reset", nil, nil, true); err != nil {
		return browserError(err)
	}
	return nil
}

func snapshot(page edgev1.PageSnapshot) PageSnapshot {
	result := PageSnapshot{
		Title: page.Title, URL: page.URL, Text: page.Text,
		TextTruncated: page.TextTruncated, ElementsTruncated: page.ElementsTruncated,
	}
	for _, element := range page.Elements {
		result.Elements = append(result.Elements, Element(element))
	}
	return result
}

// browserError says in plain words what an edge refusal means for the model.
func browserError(err error) error {
	var edgeErr *edgeclient.Error
	if !errors.As(err, &edgeErr) {
		return fmt.Errorf("browser: %w", err)
	}
	switch edgeErr.Code {
	case edgev1.CodeConflict:
		return fmt.Errorf("browser: a member has taken over this browser; wait until they hand it back (%s)", edgeErr.Message)
	case edgev1.CodeUnavailable:
		return fmt.Errorf("browser: the browser is unavailable right now, try again shortly (%s)", edgeErr.Message)
	default:
		return fmt.Errorf("browser: %s", edgeErr.Message)
	}
}

// validate refuses a request the browser must not be sent, as
// agentcorebrowser does: a navigation's host is looked up, and every address
// it answers must be public.
func (request Request) validate(ctx context.Context, resolver safehttp.Resolver) error {
	switch request.Action {
	case Navigate:
		if len(request.URL) > maxURLBytes {
			return fmt.Errorf("URL exceeds the %d-byte browser input limit", maxURLBytes)
		}
		parsed, err := url.ParseRequestURI(strings.TrimSpace(request.URL))
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("browser URL must be an absolute http or https URL")
		}
		if parsed.User != nil {
			return errors.New("browser URLs cannot contain credentials")
		}
		if err := checkHost(ctx, resolver, parsed.Hostname()); err != nil {
			return err
		}
	case Click:
		return validateSelector(request.Selector)
	case Fill:
		if err := validateSelector(request.Selector); err != nil {
			return err
		}
		if len(request.Text) > maxFillBytes {
			return fmt.Errorf("fill text exceeds the %d-byte browser input limit", maxFillBytes)
		}
		if strings.ContainsRune(request.Text, '\x00') || !utf8.ValidString(request.Text) {
			return errors.New("fill text must be valid UTF-8 without null bytes")
		}
	case Snapshot, Screenshot:
	default:
		return fmt.Errorf("unsupported browser action %q", request.Action)
	}
	return nil
}

func validateSelector(selector string) error {
	if strings.TrimSpace(selector) == "" {
		return errors.New("CSS selector is required")
	}
	if len(selector) > maxSelectorBytes {
		return fmt.Errorf("CSS selector exceeds the %d-byte browser input limit", maxSelectorBytes)
	}
	if strings.ContainsRune(selector, '\x00') || !utf8.ValidString(selector) {
		return errors.New("CSS selector must be valid UTF-8 without null bytes")
	}
	return nil
}
