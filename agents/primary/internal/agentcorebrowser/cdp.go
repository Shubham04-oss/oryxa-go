package agentcorebrowser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/gorilla/websocket"
)

const (
	agentCoreSigningService = "bedrock-agentcore"
	emptyPayloadSHA256      = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	defaultCommandTimeout   = 30 * time.Second
	pageReadyTimeout        = 15 * time.Second
	maxSnapshotTextBytes    = 64 * 1024
	maxSnapshotElements     = 200
	maxElementTextBytes     = 2 * 1024
	maxScreenshotBytes      = 10 * 1024 * 1024
	browserReadyTimeout     = 30 * time.Second
	browserReadyRetryDelay  = 500 * time.Millisecond
	maxErrorResponseBytes   = 8 * 1024
)

type sigV4Connector struct {
	region       string
	credentials  aws.CredentialsProvider
	signer       *v4.Signer
	dialer       *websocket.Dialer
	now          func() time.Time
	readyTimeout time.Duration
	retryDelay   time.Duration
}

func newSigV4Connector(config aws.Config) (*sigV4Connector, error) {
	if strings.TrimSpace(config.Region) == "" {
		return nil, errors.New("AWS Region is required for AgentCore browser")
	}
	if config.Credentials == nil {
		return nil, errors.New("AWS credential provider is required for AgentCore browser")
	}
	dialer := *websocket.DefaultDialer
	dialer.HandshakeTimeout = defaultCommandTimeout
	return &sigV4Connector{
		region:       config.Region,
		credentials:  config.Credentials,
		signer:       v4.NewSigner(),
		dialer:       &dialer,
		now:          time.Now,
		readyTimeout: browserReadyTimeout,
		retryDelay:   browserReadyRetryDelay,
	}, nil
}

func (c *sigV4Connector) connect(ctx context.Context, endpoint string) (automationClient, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "wss" || parsed.Host == "" {
		return nil, errors.New("AgentCore browser automation endpoint must be a wss URL")
	}
	credentials, err := c.credentials.Retrieve(ctx)
	if err != nil {
		return nil, fmt.Errorf("retrieve AWS credentials: %w", err)
	}
	readyCtx, cancel := context.WithTimeout(ctx, c.readyTimeout)
	defer cancel()

	var lastErr error
	for {
		client, retry, err := c.connectOnce(readyCtx, endpoint, credentials)
		if err == nil {
			return client, nil
		}
		if readyCtx.Err() != nil {
			return nil, errors.Join(lastErr, err, fmt.Errorf("wait for AgentCore browser resources: %w", readyCtx.Err()))
		}
		if !retry {
			return nil, err
		}
		lastErr = err

		timer := time.NewTimer(c.retryDelay)
		select {
		case <-readyCtx.Done():
			timer.Stop()
			return nil, errors.Join(lastErr, fmt.Errorf("wait for AgentCore browser resources: %w", readyCtx.Err()))
		case <-timer.C:
		}
	}
}

func (c *sigV4Connector) connectOnce(ctx context.Context, endpoint string, credentials aws.Credentials) (automationClient, bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false, fmt.Errorf("build automation stream request: %w", err)
	}
	if err := c.signer.SignHTTP(ctx, credentials, request, emptyPayloadSHA256, agentCoreSigningService, c.region, c.now()); err != nil {
		return nil, false, fmt.Errorf("sign automation stream request: %w", err)
	}
	connection, response, err := c.dialer.DialContext(ctx, endpoint, request.Header)
	if err != nil {
		if response != nil {
			defer response.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorResponseBytes))
			detail := strings.TrimSpace(string(body))
			if detail != "" {
				connectErr := fmt.Errorf("open automation stream: HTTP %s: %s: %w", response.Status, detail, err)
				return nil, response.StatusCode == http.StatusNotFound && strings.Contains(detail, "Required resources not found for session"), connectErr
			}
			return nil, false, fmt.Errorf("open automation stream: HTTP %s: %w", response.Status, err)
		}
		return nil, false, fmt.Errorf("open automation stream: %w", err)
	}
	client := &cdpClient{connection: connection, commandTimeout: defaultCommandTimeout}
	if err := client.initialize(ctx); err != nil {
		_ = connection.Close()
		return nil, false, err
	}
	return client, false, nil
}

type webSocket interface {
	WriteJSON(any) error
	ReadJSON(any) error
	SetWriteDeadline(time.Time) error
	SetReadDeadline(time.Time) error
	Close() error
}

type cdpClient struct {
	connection     webSocket
	commandTimeout time.Duration
	nextID         int64
	sessionID      string
}

func (c *cdpClient) initialize(ctx context.Context) error {
	var targets struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
			URL      string `json:"url"`
		} `json:"targetInfos"`
	}
	if err := c.command(ctx, "Target.getTargets", nil, false, &targets); err != nil {
		return fmt.Errorf("discover browser page: %w", err)
	}
	var targetID string
	for _, target := range targets.TargetInfos {
		if target.Type == "page" {
			targetID = target.TargetID
			break
		}
	}
	if targetID == "" {
		var created struct {
			TargetID string `json:"targetId"`
		}
		if err := c.command(ctx, "Target.createTarget", map[string]any{"url": "about:blank"}, false, &created); err != nil {
			return fmt.Errorf("create browser page: %w", err)
		}
		targetID = created.TargetID
	}
	if targetID == "" {
		return errors.New("AgentCore browser exposed no page target")
	}
	var attached struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.command(ctx, "Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true}, false, &attached); err != nil {
		return fmt.Errorf("attach to browser page: %w", err)
	}
	if attached.SessionID == "" {
		return errors.New("AgentCore browser returned no CDP page session")
	}
	c.sessionID = attached.SessionID
	for _, method := range []string{"Page.enable", "Runtime.enable"} {
		if err := c.command(ctx, method, nil, true, nil); err != nil {
			return fmt.Errorf("initialize browser page: %w", err)
		}
	}
	return nil
}

func (c *cdpClient) invoke(ctx context.Context, request Request) (Result, error) {
	switch request.Action {
	case Navigate:
		return c.navigate(ctx, strings.TrimSpace(request.URL))
	case Snapshot:
		page, err := c.snapshot(ctx)
		return Result{Page: page}, err
	case Click:
		return c.click(ctx, request.Selector)
	case Fill:
		return c.fill(ctx, request.Selector, request.Text)
	case Screenshot:
		image, err := c.screenshot(ctx)
		return Result{ScreenshotPNG: image}, err
	default:
		return Result{}, fmt.Errorf("unsupported browser action %q", request.Action)
	}
}

func (c *cdpClient) navigate(ctx context.Context, targetURL string) (Result, error) {
	var navigation struct {
		ErrorText string `json:"errorText"`
	}
	if err := c.command(ctx, "Page.navigate", map[string]any{"url": targetURL}, true, &navigation); err != nil {
		return Result{}, err
	}
	if navigation.ErrorText != "" {
		return Result{}, fmt.Errorf("navigation failed: %s", navigation.ErrorText)
	}
	if err := c.waitUntilReady(ctx); err != nil {
		return Result{}, err
	}
	page, err := c.snapshot(ctx)
	return Result{Page: page}, err
}

func (c *cdpClient) click(ctx context.Context, selector string) (Result, error) {
	literal, _ := json.Marshal(selector)
	expression := fmt.Sprintf(`(() => {
  const element = document.querySelector(%s);
  if (!element) throw new Error("No element matches the CSS selector");
  if (!(element instanceof HTMLElement)) throw new Error("Matched node is not an HTML element");
  element.scrollIntoView({block: "center", inline: "center"});
  element.click();
  return true;
})()`, literal)
	if _, err := c.evaluate(ctx, expression); err != nil {
		return Result{}, err
	}
	if err := c.waitUntilReady(ctx); err != nil {
		return Result{}, err
	}
	page, err := c.snapshot(ctx)
	return Result{Page: page}, err
}

func (c *cdpClient) fill(ctx context.Context, selector, text string) (Result, error) {
	selectorLiteral, _ := json.Marshal(selector)
	textLiteral, _ := json.Marshal(text)
	expression := fmt.Sprintf(`(() => {
  const element = document.querySelector(%s);
  if (!element) throw new Error("No element matches the CSS selector");
  if (!(element instanceof HTMLElement)) throw new Error("Matched node is not an HTML element");
  if (element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement) {
    if (element.disabled || element.readOnly) throw new Error("Matched field is disabled or read-only");
    if (element instanceof HTMLInputElement && ["password", "file"].includes(element.type.toLowerCase())) {
      throw new Error("Browser tools never fill password or file-input fields");
    }
    const autocomplete = (element.getAttribute("autocomplete") || "").toLowerCase();
    if (["current-password", "new-password", "one-time-code", "cc-number", "cc-csc", "cc-exp", "cc-exp-month", "cc-exp-year"].includes(autocomplete)) {
      throw new Error("Browser tools never fill sensitive or payment fields");
    }
    const prototype = element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    const setter = Object.getOwnPropertyDescriptor(prototype, "value")?.set;
    if (setter) setter.call(element, %s); else element.value = %s;
  } else if (element.isContentEditable) {
    element.textContent = %s;
  } else {
    throw new Error("Matched element is not an input, textarea, or editable element");
  }
  element.focus();
  element.dispatchEvent(new InputEvent("input", {bubbles: true, inputType: "insertText", data: %s}));
  element.dispatchEvent(new Event("change", {bubbles: true}));
  return true;
})()`, selectorLiteral, textLiteral, textLiteral, textLiteral, textLiteral)
	if _, err := c.evaluate(ctx, expression); err != nil {
		return Result{}, err
	}
	page, err := c.snapshot(ctx)
	return Result{Page: page}, err
}

func (c *cdpClient) screenshot(ctx context.Context) ([]byte, error) {
	var captured struct {
		Data string `json:"data"`
	}
	if err := c.command(ctx, "Page.captureScreenshot", map[string]any{
		"format":                "png",
		"fromSurface":           true,
		"captureBeyondViewport": false,
	}, true, &captured); err != nil {
		return nil, err
	}
	if captured.Data == "" {
		return nil, errors.New("browser returned an empty screenshot")
	}
	image, err := base64.StdEncoding.DecodeString(captured.Data)
	if err != nil {
		return nil, fmt.Errorf("decode browser screenshot: %w", err)
	}
	if len(image) > maxScreenshotBytes {
		return nil, fmt.Errorf("browser screenshot exceeds the %d-byte artifact limit", maxScreenshotBytes)
	}
	return image, nil
}

func (c *cdpClient) snapshot(ctx context.Context) (PageSnapshot, error) {
	value, err := c.evaluate(ctx, snapshotExpression)
	if err != nil {
		return PageSnapshot{}, err
	}
	var page PageSnapshot
	if err := json.Unmarshal(value, &page); err != nil {
		return PageSnapshot{}, fmt.Errorf("decode semantic browser snapshot: %w", err)
	}
	normalizeSnapshot(&page)
	return page, nil
}

func (c *cdpClient) waitUntilReady(ctx context.Context) error {
	waitCtx, cancel := context.WithTimeout(ctx, pageReadyTimeout)
	defer cancel()
	var lastErr error
	for {
		value, err := c.evaluate(waitCtx, "document.readyState")
		if err == nil {
			var state string
			if json.Unmarshal(value, &state) == nil && (state == "interactive" || state == "complete") {
				return nil
			}
		} else {
			// A broken stream won't recover by waiting.
			if isTransportError(err) {
				return fmt.Errorf("wait for browser page: %w", err)
			}
			// A navigation can briefly destroy the prior execution context. Retrying
			// is safe because this check never changes page state.
			lastErr = err
		}
		select {
		case <-waitCtx.Done():
			if lastErr != nil {
				return fmt.Errorf("wait for browser page: %w", lastErr)
			}
			return fmt.Errorf("wait for browser page: %w", waitCtx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type runtimeEvaluateResponse struct {
	Result struct {
		Value       json.RawMessage `json:"value"`
		Description string          `json:"description"`
	} `json:"result"`
	ExceptionDetails *struct {
		Text      string `json:"text"`
		Exception *struct {
			Description string `json:"description"`
		} `json:"exception"`
	} `json:"exceptionDetails"`
}

func (c *cdpClient) evaluate(ctx context.Context, expression string) (json.RawMessage, error) {
	var response runtimeEvaluateResponse
	if err := c.command(ctx, "Runtime.evaluate", map[string]any{
		"expression":    expression,
		"awaitPromise":  true,
		"returnByValue": true,
		"userGesture":   false,
	}, true, &response); err != nil {
		return nil, err
	}
	if response.ExceptionDetails != nil {
		message := response.ExceptionDetails.Text
		if response.ExceptionDetails.Exception != nil && response.ExceptionDetails.Exception.Description != "" {
			message = response.ExceptionDetails.Exception.Description
		}
		return nil, fmt.Errorf("page script failed: %s", message)
	}
	if response.Result.Value == nil {
		return nil, errors.New("page script returned no serializable value")
	}
	return response.Result.Value, nil
}

// transportError is a failure of the automation WebSocket itself (a send,
// a receive or a deadline), as opposed to an error the browser answered. The
// connection is unusable after one: gorilla treats a missed deadline as
// final.
type transportError struct{ err error }

func (e *transportError) Error() string { return e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

// isTransportError reports whether err means the automation stream broke and
// must be reconnected before the next command.
func isTransportError(err error) bool {
	var transport *transportError
	return errors.As(err, &transport)
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *cdpClient) command(ctx context.Context, method string, params any, attached bool, output any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.nextID++
	id := c.nextID
	message := map[string]any{"id": id, "method": method}
	if params != nil {
		message["params"] = params
	}
	if attached {
		if c.sessionID == "" {
			return errors.New("CDP page session is not initialized")
		}
		message["sessionId"] = c.sessionID
	}
	deadline := time.Now().Add(c.commandTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := c.connection.SetWriteDeadline(deadline); err != nil {
		return &transportError{err}
	}
	if err := c.connection.WriteJSON(message); err != nil {
		return &transportError{fmt.Errorf("send CDP %s: %w", method, err)}
	}
	if err := c.connection.SetReadDeadline(deadline); err != nil {
		return &transportError{err}
	}
	for {
		var response struct {
			ID     *int64          `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *cdpError       `json:"error"`
		}
		if err := c.connection.ReadJSON(&response); err != nil {
			return &transportError{fmt.Errorf("receive CDP %s: %w", method, err)}
		}
		if response.ID == nil || *response.ID != id {
			continue
		}
		if response.Error != nil {
			return fmt.Errorf("CDP %s failed (%d): %s", method, response.Error.Code, response.Error.Message)
		}
		if output == nil || len(response.Result) == 0 {
			return nil
		}
		if err := json.Unmarshal(response.Result, output); err != nil {
			return fmt.Errorf("decode CDP %s response: %w", method, err)
		}
		return nil
	}
}

func (c *cdpClient) close() error {
	return c.connection.Close()
}

func normalizeSnapshot(page *PageSnapshot) {
	page.Title, _ = truncateUTF8(page.Title, maxElementTextBytes)
	page.URL, _ = truncateUTF8(page.URL, maxURLBytes)
	page.Text, page.TextTruncated = truncateUTF8(page.Text, maxSnapshotTextBytes)
	if len(page.Elements) > maxSnapshotElements {
		page.Elements = page.Elements[:maxSnapshotElements]
		page.ElementsTruncated = true
	}
	for index := range page.Elements {
		element := &page.Elements[index]
		element.Selector, _ = truncateUTF8(element.Selector, maxSelectorBytes)
		element.Tag, _ = truncateUTF8(element.Tag, 64)
		element.Role, _ = truncateUTF8(element.Role, 128)
		element.Type, _ = truncateUTF8(element.Type, 128)
		element.Name, _ = truncateUTF8(element.Name, maxElementTextBytes)
		element.Text, _ = truncateUTF8(element.Text, maxElementTextBytes)
		element.Href, _ = truncateUTF8(element.Href, maxURLBytes)
	}
}

func truncateUTF8(value string, limit int) (string, bool) {
	if len(value) <= limit {
		return value, false
	}
	value = value[:limit]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value, true
}

const snapshotExpression = `(() => {
  const escape = (value) => globalThis.CSS?.escape ? CSS.escape(value) : String(value).replace(/[^a-zA-Z0-9_-]/g, "\\$&");
  const selectorFor = (element) => {
    if (element.id) return "#" + escape(element.id);
    for (const attribute of ["data-testid", "data-test", "name", "aria-label"]) {
      const value = element.getAttribute(attribute);
      if (value) return element.tagName.toLowerCase() + "[" + attribute + "=" + JSON.stringify(value) + "]";
    }
    const parts = [];
    let current = element;
    while (current && current.nodeType === Node.ELEMENT_NODE && current !== document.documentElement && parts.length < 6) {
      const tag = current.tagName.toLowerCase();
      const siblings = current.parentElement ? Array.from(current.parentElement.children).filter((node) => node.tagName === current.tagName) : [];
      const suffix = siblings.length > 1 ? ":nth-of-type(" + (siblings.indexOf(current) + 1) + ")" : "";
      parts.unshift(tag + suffix);
      current = current.parentElement;
    }
    return parts.join(" > ");
  };
  const isVisible = (element) => {
    const style = getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    return style.visibility !== "hidden" && style.display !== "none" && rect.width > 0 && rect.height > 0;
  };
  const candidates = Array.from(document.querySelectorAll("a[href], button, input, textarea, select, [role=button], [role=link], [contenteditable=true]"));
  const visible = candidates.filter(isVisible);
  const elements = visible.slice(0, 200).map((element) => ({
    selector: selectorFor(element),
    tag: element.tagName.toLowerCase(),
    role: element.getAttribute("role") || "",
    type: element.getAttribute("type") || "",
    name: element.getAttribute("aria-label") || element.getAttribute("name") || element.getAttribute("placeholder") || "",
    text: (element.innerText || element.textContent || "").trim().slice(0, 2048),
    href: element instanceof HTMLAnchorElement ? element.href : "",
    disabled: Boolean(element.disabled || element.getAttribute("aria-disabled") === "true")
  }));
  return {
    title: document.title || "",
    url: location.href,
    text: (document.body?.innerText || "").slice(0, 65537),
    elements,
    elements_truncated: visible.length > elements.length
  };
})()`
