package toolkit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// The rest of the shape: the reader, the polled and pushed events, and the
// two helpers every one of them needs. Copy these beside toolkit.go, rename
// the package, and fill them in.

// Targets lists what the connection could index, so a member chooses from a
// list rather than typing a repository id and being told afterwards that it
// was wrong. It is a read, and its session refuses everything but a read.
func (Integration) Targets(ctx context.Context, session integration.Session) ([]integration.Target, error) {
	raw, err := call(ctx, session, "GET", session.BaseURL+"/user/repos?per_page=100", nil)
	if err != nil {
		return nil, err
	}
	var listed []struct {
		FullName    string `json:"full_name"`
		Description string `json:"description"`
		Archived    bool   `json:"archived"`
	}
	if json.Unmarshal(raw, &listed) != nil {
		return nil, errors.New("the repository list could not be read")
	}
	targets := make([]integration.Target, 0, len(listed))
	for _, repository := range listed {
		if repository.FullName == "" || repository.Archived {
			continue
		}
		targets = append(targets, integration.Target{
			Kind: "repo", ID: repository.FullName, Name: repository.FullName,
			Description: repository.Description,
			Target:      json.RawMessage(`{"repo":"` + repository.FullName + `"}`),
		})
	}
	return targets, nil
}

// Read answers one page of one target, which is what a member's indexing pass
// walks. The provider's own paging goes in Page.Cursor, and an incremental
// pass reads by Page.Watermark or Page.Token when the app has one: there is
// no Held, and knowledge's content hash keeps a document that has not changed
// from being published twice.
//
// A page must stay under integration.MaxPageBytes. One item is often a whole
// file, so a reader that cannot fit a page takes fewer items rather than
// returning one too large.
func (Integration) Read(ctx context.Context, session integration.Session, asked integration.PageRequest) (integration.Page, error) {
	repo := asked.Target["repo"]
	if repo == "" {
		return integration.Page{}, &integration.AppError{Message: "the page request names no repository"}
	}
	page := integration.Page{Items: []integration.Item{}}
	path := session.BaseURL + "/repos/" + repo + "/issues"
	switch {
	case asked.Cursor != "":
		// A pass under way carries on from where it was.
		path += "?page=" + asked.Cursor
	case asked.Watermark != nil:
		path += "?since=" + asked.Watermark.UTC().Format(time.RFC3339)
	}
	raw, err := call(ctx, session, "GET", path, nil)
	if err != nil {
		return integration.Page{}, err
	}
	var issues []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	}
	if json.Unmarshal(raw, &issues) != nil {
		return integration.Page{}, errors.New("the issue list could not be read")
	}
	size := asked.Limit
	if size <= 0 || size > maxPageSize {
		size = maxPageSize
	}
	for _, issue := range issues {
		if len(page.Items) == size {
			break
		}
		page.Items = append(page.Items, integration.Item{
			ExternalID: repo + "#" + itoa(issue.Number),
			Kind:       "issue",
			Title:      issue.Title,
			Content:    []byte(issue.Body),
		})
	}
	// A page that came back full may have more behind it, and a cursor is how
	// the next pass finds it. A page that came back part-full is the end: an
	// answer that always carries a cursor never drains a target at all.
	if len(issues) >= size {
		page.Cursor = itoa(size + 1)
	}
	return page, nil
}

// Poll asks the app what has changed since the cursor it answered last, and is
// the only way an event arrives for an app that pushes nothing.
//
// The first poll carries no cursor, and the contract the harness checks is
// that it answers no events and a cursor: a member who subscribes to an event
// starts hearing about it from then on, rather than being handed everything
// the app has ever recorded. Inventing a cursor, or answering events on the
// first pass, is the mistake that rule is about.
func (Integration) Poll(ctx context.Context, session integration.Session, _ string, config json.RawMessage, cursor string) (integration.Batch, error) {
	if cursor != "" {
		raw, err := call(ctx, session, "GET", session.BaseURL+"/issues/updates?since="+cursor, nil)
		if err != nil {
			return integration.Batch{}, err
		}
		var answered struct {
			Cursor string `json:"cursor"`
			Events []struct {
				ID    string `json:"id"`
				At    string `json:"occurred_at"`
				State string `json:"state"`
				Num   int    `json:"number"`
			} `json:"events"`
		}
		if json.Unmarshal(raw, &answered) != nil {
			return integration.Batch{}, errors.New("the updates could not be read")
		}
		batch := integration.Batch{Cursor: answered.Cursor}
		for _, event := range answered.Events {
			at, err := time.Parse(time.RFC3339, event.At)
			if err != nil {
				continue
			}
			data, err := json.Marshal(map[string]any{"number": event.Num, "state": event.State})
			if err != nil {
				continue
			}
			batch.Events = append(batch.Events, integration.Event{
				ID: event.ID, Trigger: "ACME_ISSUE_EVENT", OccurredAt: at, Data: data,
			})
		}
		return batch, nil
	}
	// The first poll: a cursor and nothing else.
	raw, err := call(ctx, session, "GET", session.BaseURL+"/issues/updates", nil)
	if err != nil {
		return integration.Batch{}, err
	}
	var answered struct {
		Cursor string `json:"cursor"`
	}
	if json.Unmarshal(raw, &answered) != nil || answered.Cursor == "" {
		return integration.Batch{}, errors.New("the app answered no cursor to start from")
	}
	return integration.Batch{Cursor: answered.Cursor}, nil
}

// Subscribe registers the URL and the secret the core made, and answers the
// app's own id for the subscription so it can be removed again. The secret is
// seen exactly once: it is in Hook for this call and nowhere else, and
// integration.Hook redacts it wherever it is printed or logged.
func (Integration) Subscribe(ctx context.Context, session integration.Session, _ string, _ json.RawMessage, hook integration.Hook) (string, error) {
	body, err := json.Marshal(map[string]string{
		"url": hook.URL, "secret": hook.Secret, "events": "push",
	})
	if err != nil {
		return "", &integration.AppError{Message: "the subscription could not be written"}
	}
	raw, err := call(ctx, session, "POST", session.BaseURL+"/hooks", body)
	if err != nil {
		return "", err
	}
	var created struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &created) != nil || created.ID == "" {
		return "", errors.New("the app answered no id for the subscription")
	}
	return created.ID, nil
}

// Unsubscribe removes the subscription at the app, so it stops delivering.
func (Integration) Unsubscribe(ctx context.Context, session integration.Session, remoteID string) error {
	_, err := call(ctx, session, "DELETE", session.BaseURL+"/hooks/"+remoteID, nil)
	return err
}

// Parse reads a delivery the core has already verified. It only reads: no
// call, no signature check, and it never sees the delivery's Authorization or
// Cookie headers, which the core removes before it gets here.
//
// Every event it yields is validated against its trigger's payload_schema by
// the core before it is published, so what is put in Data has to be the whole
// contract and not a summary of it. An event with no id is delivered twice on
// a redelivery, so one is required.
func (Integration) Parse(_ context.Context, header http.Header, body []byte) ([]integration.Event, error) {
	if got := header.Get("X-Acme-Event"); got != "push" {
		return nil, nil
	}
	var delivery struct {
		ID  string `json:"id"`
		At  string `json:"occurred_at"`
		Ref string `json:"ref"`
	}
	if json.Unmarshal(body, &delivery) != nil {
		return nil, errors.New("the delivery could not be read")
	}
	at, err := time.Parse(time.RFC3339, delivery.At)
	if err != nil {
		return nil, errors.New("the delivery carries no time")
	}
	data, err := json.Marshal(map[string]string{"ref": delivery.Ref})
	if err != nil {
		return nil, err
	}
	return []integration.Event{{
		ID: delivery.ID, Trigger: "ACME_REPO_EVENT", OccurredAt: at, Data: data,
	}}, nil
}

// call is every request the toolkit makes: one method, one URL, one optional
// body, over the Session. The credential is already placed in the innermost
// transport, so nothing here sets a header the app needs, and a read tool's
// session has already refused anything but a read.
//
// The four ways an app refuses are the four the core acts on, and each is
// returned as its own type: anything else is an *integration.AppError, which
// the core answers to the caller as successful false with the reason, rather
// than as a failure it cannot act on.
func call(ctx context.Context, session integration.Session, method, target string, body []byte) (json.RawMessage, error) {
	var reader io.Reader
	if body != nil {
		reader = strings.NewReader(string(body))
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, &integration.AppError{Message: "the request could not be written"}
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := session.HTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	read, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return nil, errors.New("the answer could not be read")
	}
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return read, nil
	case response.StatusCode == http.StatusUnauthorized:
		return nil, integration.ErrNeedsReconnect
	case response.StatusCode == http.StatusTooManyRequests:
		limited := &integration.RateLimited{}
		if after := response.Header.Get("Retry-After"); after != "" {
			if seconds, err := strconv.Atoi(after); err == nil {
				limited.RetryAfter = time.Duration(seconds) * time.Second
			}
		}
		return nil, limited
	case response.StatusCode == http.StatusNotFound:
		return nil, integration.ErrNotFound
	}
	// Anything else is the app refusing in its own words, which is what the
	// caller reads to try again differently. It never carries a credential:
	// the app's own error body does not, and anything it did is not copied
	// into the message.
	var refusal struct {
		Message string `json:"message"`
	}
	message := strings.TrimSpace(string(read))
	if json.Unmarshal(read, &refusal) == nil && refusal.Message != "" {
		message = refusal.Message
	}
	return nil, &integration.AppError{Message: message}
}

// maxPageSize is the most issues one page asks for, which keeps a page of
// documents inside integration.MaxPageBytes whatever each of them weighs.
const maxPageSize = 50

func itoa(value int) string { return strconv.Itoa(value) }
