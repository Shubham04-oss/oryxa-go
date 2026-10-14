package v1

// The browser API (contract §6): Go's cfbrowser calls these on a BrowserDO,
// through the room, at BrowserPath(room, session) + the suffix on each type.
//
// Elements are addressed by the CSS selector the latest snapshot gave them,
// exactly as the browser_* tools take them today, so the tools' shapes don't
// change.

// NavigateRequest is POST …/navigate.
type NavigateRequest struct {
	URL string `json:"url"`
}

// ClickRequest is POST …/click.
type ClickRequest struct {
	Selector string `json:"selector"`
}

// FillRequest is POST …/fill.
type FillRequest struct {
	Selector string `json:"selector"`
	Text     string `json:"text"`
}

// Element is one visible, actionable element in a page snapshot.
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

// PageSnapshot is the page's bounded text and actionable elements. It is
// untrusted web content.
type PageSnapshot struct {
	Title             string    `json:"title"`
	URL               string    `json:"url"`
	Text              string    `json:"text,omitempty"`
	Elements          []Element `json:"elements,omitempty"`
	TextTruncated     bool      `json:"textTruncated,omitempty"`
	ElementsTruncated bool      `json:"elementsTruncated,omitempty"`
}

// PageResponse answers navigate, snapshot, click and fill.
// SessionRecreated means a new browser session had to start, so cookies and
// the earlier page are gone.
type PageResponse struct {
	Page             PageSnapshot `json:"page"`
	SessionRecreated bool         `json:"sessionRecreated,omitempty"`
}

// ScreenshotResponse answers POST …/screenshot. The PNG is in R2 at Key
// (media/ under the run); Data carries it as base64 too, so the agent can
// keep it as an artifact without a second call.
type ScreenshotResponse struct {
	Key              string `json:"key"`
	MIMEType         string `json:"mimeType"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	Data             string `json:"data"`
	SessionRecreated bool   `json:"sessionRecreated,omitempty"`
}

// LiveRequest is POST …/live. Mode is "tab" (the default) or "full".
type LiveRequest struct {
	Mode string `json:"mode,omitempty"`
}

// HandoffRequest is POST …/handoff: ask a member to take over, for instance
// at a login. TimeoutMs is capped at 30 minutes.
type HandoffRequest struct {
	Reason    string `json:"reason"`
	TimeoutMs int64  `json:"timeoutMs,omitempty"`
}

// HandoffResponse is the open handoff and where a member takes over.
type HandoffResponse struct {
	HandoffID string    `json:"handoffId"`
	Live      SignedURL `json:"live"`
}

// HandoffState answers GET …/handoff: whether a member still has control.
type HandoffState struct {
	Active    bool   `json:"active"`
	HandoffID string `json:"handoffId,omitempty"`
	Reason    string `json:"reason,omitempty"`
	ElapsedMs int64  `json:"elapsedMs,omitempty"`
}

// ResumeRequest is POST …/resume: the member hands control back.
type ResumeRequest struct {
	Success bool   `json:"success"`
	Note    string `json:"note,omitempty"`
}

// ResumeResponse answers POST …/resume.
type ResumeResponse struct {
	Resumed bool         `json:"resumed"`
	Page    PageSnapshot `json:"page"`
}
