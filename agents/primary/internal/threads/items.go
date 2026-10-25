package threads

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// The kinds of item a thread's history and a run's stream are made of. A
// thread's history holds messages, tool calls, tool results, confirmations
// and each run's start and end; a run's stream carries the same items as they
// happen, plus deltas.
const (
	// ItemMessage is text from the member or from Krude.
	ItemMessage = "message"
	// ItemDelta is a piece of Krude's answer while it is still being
	// written. The message item that follows carries the whole text.
	ItemDelta = "delta"
	// ItemToolCall is Krude calling a tool, a sub-agent or a helper.
	ItemToolCall = "tool_call"
	// ItemToolResult is a tool call finishing.
	ItemToolResult = "tool_result"
	// ItemConfirmation is a tool waiting for the member to approve or
	// decline it before it runs.
	ItemConfirmation = "confirmation"
	// ItemArtifact is a file a run kept, such as a browser screenshot or a
	// file the sandbox saved, readable through the thread's artifact route.
	ItemArtifact = "artifact"
	// ItemRequest is a pointer to an agent request: a metadata-only Requests
	// note, or a successful interactions_create result in its originating
	// conversation. The client reads the live card with the member's session.
	ItemRequest = "request"
	// ItemRunStarted, ItemRunCompleted and ItemRunFailed bracket a run.
	ItemRunStarted   = "run_started"
	ItemRunCompleted = "run_completed"
	ItemRunFailed    = "run_failed"
)

// The states of a confirmation.
const (
	ConfirmationPending  = "pending"
	ConfirmationApproved = "approved"
	ConfirmationDeclined = "declined"
	// ConfirmationExpired is a confirmation nobody answered within
	// ConfirmationTTL. Its tool never runs, and it no longer holds the
	// thread: the member's next message goes ahead.
	ConfirmationExpired = "expired"
)

// ConfirmationTTL is how long a confirmation waits for the member. One left
// longer is expired: it would otherwise hold its thread forever, refusing
// every message, and keep the thread at the top of what needs the member.
const ConfirmationTTL = 24 * time.Hour

// expiredKey marks the answer Oryxa stores for an expired confirmation, so
// the history tells it from one the member declined.
const expiredKey = "expired"

// AuthorMember is who a member's own messages are from, and AuthorApps who
// sent a message carrying events from their connected apps. Everything else
// is from the agent that wrote it, which in a thread is Krude.
const (
	AuthorMember = "member"
	AuthorApps   = "apps"
)

// adkUserAuthor is how ADK names the author of what a member sends.
const adkUserAuthor = "user"

const (
	// maxArgumentBytes bounds a tool call's arguments in an item. A call
	// with more, such as a long program for the sandbox, is shown without
	// them. A confirmation always carries its arguments: the member cannot
	// approve what they cannot see.
	maxArgumentBytes = 16 << 10
	// maxErrorRunes bounds the error a failed tool result carries.
	maxErrorRunes = 500
)

// Item is one thing that happened in a thread. Type says which fields are
// set.
type Item struct {
	Type string `json:"type"`
	// ID is the stored event the item came from, so a client can match what
	// it streamed against the history it reads later. Deltas have none; a
	// run's brackets have their run's id with ".started" or ".ended", streamed
	// and stored alike (brackets.go).
	ID string    `json:"id,omitempty"`
	At time.Time `json:"at,omitzero"`
	// Author is AuthorMember or the agent's name.
	Author string `json:"author,omitempty"`
	Text   string `json:"text,omitempty"`
	// Error says why a message, tool result or run went wrong. On a failed
	// run, Code says it as one of the Failure codes, under the name a chat
	// Durable Object gives the codes it writes on a run_failed of its own
	// (session_expired, or Go's refusal), which none of them shares.
	Error string `json:"error,omitempty"`
	Code  string `json:"code,omitempty"`
	// CallID ties a tool result to its call. On a confirmation it is the ID
	// the member's answer names, and ToolCallID the call it holds back, so a
	// client can show that call as waiting rather than running.
	CallID     string `json:"call_id,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Tool       string `json:"tool,omitempty"`
	// Arguments are what the tool was called with.
	Arguments map[string]any `json:"arguments,omitempty"`
	// Failed marks a tool result that ended in an error.
	Failed bool `json:"failed,omitempty"`
	// Hint and Status describe a confirmation. On a request item, Status says
	// how the request stood when the line was written (the Note constants).
	Hint   string `json:"hint,omitempty"`
	Status string `json:"status,omitempty"`
	// RequestID is the agent request a request item is about.
	RequestID string `json:"request_id,omitempty"`
	// Artifact and Version name a file a run kept.
	Artifact string `json:"artifact,omitempty"`
	Version  int64  `json:"version,omitempty"`
	// RunID and Usage describe a run.
	RunID string          `json:"run_id,omitempty"`
	Usage *agentsv1.Usage `json:"usage,omitempty"`
	// Interrupted marks a run that failed because Oryxa was restarting
	// (ErrInterrupted, ErrDraining): not of its own doing, and worth starting
	// again.
	Interrupted bool `json:"interrupted,omitempty"`
}

// streamItems is what one event a run yields shows the member while the run
// happens: the text of a partial event as a delta, and a finished event's
// items with every confirmation still pending.
func streamItems(event *session.Event) []Item {
	if event == nil {
		return nil
	}
	if event.Partial {
		text := eventText(event)
		if text == "" {
			return nil
		}
		return []Item{{Type: ItemDelta, At: at(event), Author: authorOf(event), Text: text}}
	}
	return eventItems(event, nil)
}

// historyItems is a thread's history: every stored event's items, each
// confirmation with the answer the member gave it, if any.
//
// A run's start is stored before the runner appends what started it: the
// member's message, an apps batch or the member's answers. A run's stream
// sends that first and the start after it, so history lists the start where
// the stream has it, after what started the run and before anything the run
// added, which a client groups into the turn of the prompt before it. Its At
// still says when it was stored.
func historyItems(events []*session.Event) []Item {
	answers := confirmationAnswers(events)
	items := make([]Item, 0, len(events))
	var started *Item
	for _, event := range events {
		if event == nil || event.Partial {
			continue
		}
		if bracket, ok := bracketOf(event); ok && bracket.Type == ItemRunStarted {
			if started != nil {
				items = append(items, *started)
			}
			started = &bracket
			continue
		}
		if started != nil && !startsRun(event) {
			items = append(items, *started)
			started = nil
		}
		items = append(items, eventItems(event, answers)...)
	}
	if started != nil {
		items = append(items, *started)
	}
	return items
}

// startsRun reports whether an event is what the member, or their apps, sent
// a run: content of ADK's user author, which only the runner appends from a
// run's input. A bracket, a note or a rename is the user author's too, but
// carries no content.
func startsRun(event *session.Event) bool {
	return event.Author == adkUserAuthor && event.Content != nil
}

// displayAgent replaces only the answering agent's immutable ADK name with
// its renameable member-facing identity. Sub-agent authors remain distinct.
func displayAgent(items []Item, machineName, displayName string) []Item {
	if displayName == "" || displayName == machineName {
		return items
	}
	for index := range items {
		if items[index].Author == machineName {
			items[index].Author = displayName
		}
	}
	return items
}

// eventItems turns one finished event into items, in the order its parts
// came in. Confirmations take their status from answers; a confirmation
// answers does not hold is pending.
func eventItems(event *session.Event, answers map[string]string) []Item {
	if bracket, ok := bracketOf(event); ok {
		return []Item{bracket}
	}
	// A note is an event of metadata alone. It is read before anything else,
	// and a message a model or a member wrote never carries one.
	if note, ok := noteOf(event); ok && event.Content == nil {
		return []Item{{
			Type: ItemRequest, ID: event.ID, At: at(event), Author: event.Author,
			Text: note.Text, Status: note.State, RequestID: note.RequestID,
		}}
	}
	author := authorOf(event)
	var items []Item
	message := -1
	var parts []*genai.Part
	if event.Content != nil {
		parts = event.Content.Parts
	}
	for _, part := range parts {
		switch {
		case part == nil || part.Thought:
			continue
		case part.Text != "":
			if message < 0 {
				message = len(items)
				items = append(items, Item{Type: ItemMessage, ID: event.ID, At: at(event), Author: author})
			}
			items[message].Text += part.Text
		case part.FunctionCall != nil:
			if part.FunctionCall.Name == toolconfirmation.FunctionCallName {
				if item, ok := confirmationItem(event, part.FunctionCall, answers); ok {
					items = append(items, item)
				}
				continue
			}
			items = append(items, Item{
				Type: ItemToolCall, ID: event.ID, At: at(event), Author: author,
				CallID: part.FunctionCall.ID, Tool: part.FunctionCall.Name,
				Arguments: boundedArguments(part.FunctionCall.Args),
			})
		case part.FunctionResponse != nil:
			response := part.FunctionResponse
			// The member's answer to a confirmation shows as the
			// confirmation's status, and a tool's reply that it is waiting
			// for one is not its result: the result comes once it runs.
			if response.Name == toolconfirmation.FunctionCallName {
				continue
			}
			if _, waiting := event.Actions.RequestedToolConfirmations[response.ID]; waiting {
				continue
			}
			item := Item{
				Type: ItemToolResult, ID: event.ID, At: at(event), Author: author,
				CallID: response.ID, Tool: response.Name,
			}
			if failure := responseError(response.Response); failure != "" {
				item.Failed = true
				item.Error = failure
			}
			items = append(items, item)
			if response.Name == "interactions_create" && !item.Failed {
				// A tool's response may contain private context or a held answer.
				// Expose only its request pointer; Go authorizes the card's own read.
				var request struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				}
				if decodeInto(response.Response, &request) && request.ID != "" {
					items = append(items, Item{
						Type: ItemRequest, ID: event.ID, At: at(event), Author: author,
						RequestID: request.ID, Status: request.Status, CallID: response.ID,
					})
				}
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(event.Actions.ArtifactDelta)) {
		items = append(items, Item{
			Type: ItemArtifact, ID: event.ID, At: at(event), Author: author,
			Artifact: name, Version: event.Actions.ArtifactDelta[name],
		})
	}
	// A model that stopped without answering, blocked or cut short, says why
	// rather than leaving the member with nothing.
	if failure := strings.TrimSpace(strings.Trim(event.ErrorCode+": "+event.ErrorMessage, ": ")); failure != "" && event.Author != adkUserAuthor {
		if message < 0 {
			items = append(items, Item{Type: ItemMessage, ID: event.ID, At: at(event), Author: author})
			message = len(items) - 1
		}
		items[message].Error = failure
	}
	return items
}

// confirmationItem describes a confirmation request by the call it holds
// back: the tool, what it would be called with, and why it asks.
func confirmationItem(event *session.Event, call *genai.FunctionCall, answers map[string]string) (Item, bool) {
	original, err := toolconfirmation.OriginalCallFrom(call)
	if err != nil {
		return Item{}, false
	}
	item := Item{
		Type: ItemConfirmation, ID: event.ID, At: at(event), Author: authorOf(event),
		CallID: call.ID, ToolCallID: original.ID, Tool: original.Name, Arguments: original.Args, Status: ConfirmationPending,
	}
	var requested toolconfirmation.ToolConfirmation
	if decodeInto(call.Args["toolConfirmation"], &requested) {
		item.Hint = memberHint(requested.Hint)
	}
	if status, answered := answers[call.ID]; answered {
		item.Status = status
	}
	return item, true
}

// adkDefaultHint opens the hint ADK gives every tool that asks without saying
// why: words for a program answering it, not for the member.
const adkDefaultHint = "Please approve or reject the tool call "

// memberHint is a tool's own reason for asking, or nothing when all it has is
// ADK's default, so the client can say it in its own words instead.
func memberHint(hint string) string {
	if strings.HasPrefix(strings.TrimSpace(hint), adkDefaultHint) {
		return ""
	}
	return strings.TrimSpace(hint)
}

// confirmationAnswers collects how each answered confirmation stands —
// approved, declined or expired — by the ID of the confirmation request the
// answer names.
func confirmationAnswers(events []*session.Event) map[string]string {
	answers := map[string]string{}
	for _, event := range events {
		if event == nil || event.Author != adkUserAuthor || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part == nil || part.FunctionResponse == nil || part.FunctionResponse.Name != toolconfirmation.FunctionCallName {
				continue
			}
			response := part.FunctionResponse.Response
			switch {
			case response[expiredKey] == true:
				answers[part.FunctionResponse.ID] = ConfirmationExpired
			case confirmedIn(response):
				answers[part.FunctionResponse.ID] = ConfirmationApproved
			default:
				answers[part.FunctionResponse.ID] = ConfirmationDeclined
			}
		}
	}
	return answers
}

// waitingConfirmations lists the confirmations agentName asked for that
// nothing has answered yet, in the order they were asked: those the member
// can still answer, and those asked ConfirmationTTL or more before now,
// which have expired. Only the agent running the thread can ask its member
// to confirm something, the same rule ADK applies when it resumes one.
func waitingConfirmations(events []*session.Event, agentName string, now time.Time) (pending, expired []string) {
	answers := confirmationAnswers(events)
	for _, event := range events {
		if event == nil || event.Author != agentName || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part == nil || part.FunctionCall == nil || part.FunctionCall.Name != toolconfirmation.FunctionCallName {
				continue
			}
			if _, answered := answers[part.FunctionCall.ID]; answered {
				continue
			}
			if !event.Timestamp.IsZero() && now.Sub(event.Timestamp) >= ConfirmationTTL {
				expired = append(expired, part.FunctionCall.ID)
			} else {
				pending = append(pending, part.FunctionCall.ID)
			}
		}
	}
	return pending, expired
}

// expireInvocation names the invocation an expiry's event belongs to. It is
// no run's: it answers confirmations nobody answered in time.
const expireInvocation = "oryxa-expire-confirmations"

// expiryEvent answers the expired confirmations ids as declined, marked
// expired, so their tools never run and a later run finds nothing waiting.
// ADK leaves confirmation answers out of what the model reads, and resumes
// a tool only from the member's latest answers, so this changes nothing the
// agent sees.
func expiryEvent(ctx context.Context, ids []string) *session.Event {
	parts := make([]*genai.Part, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, &genai.Part{FunctionResponse: &genai.FunctionResponse{
			ID:       id,
			Name:     toolconfirmation.FunctionCallName,
			Response: map[string]any{"confirmed": false, expiredKey: true},
		}})
	}
	event := session.NewEvent(ctx, expireInvocation)
	event.Author = adkUserAuthor
	event.Content = &genai.Content{Role: genai.RoleUser, Parts: parts}
	return event
}

// expireItems marks the confirmations in expired as expired in items.
func expireItems(items []Item, expired []string) []Item {
	for index := range items {
		if items[index].Type == ItemConfirmation && slices.Contains(expired, items[index].CallID) {
			items[index].Status = ConfirmationExpired
		}
	}
	return items
}

// confirmedIn reads a confirmation answer the way ADK does: the decision
// itself, or ADK's web client's form, a JSON string under "response".
func confirmedIn(response map[string]any) bool {
	var decision toolconfirmation.ToolConfirmation
	if encoded, ok := response["response"].(string); ok && len(response) == 1 {
		if json.Unmarshal([]byte(encoded), &decision) != nil {
			return false
		}
		return decision.Confirmed
	}
	if !decodeInto(response, &decision) {
		return false
	}
	return decision.Confirmed
}

// eventText is the text an event carries, without the model's thoughts.
func eventText(event *session.Event) string {
	if event.Content == nil {
		return ""
	}
	var text strings.Builder
	for _, part := range event.Content.Parts {
		if part != nil && !part.Thought {
			text.WriteString(part.Text)
		}
	}
	return text.String()
}

// at is when an event happened, in UTC like everything else the API
// answers: ADK stamps events in the server's own zone.
func at(event *session.Event) time.Time {
	if event.Timestamp.IsZero() {
		return time.Time{}
	}
	return event.Timestamp.UTC()
}

func authorOf(event *session.Event) string {
	if event.Author != adkUserAuthor {
		return event.Author
	}
	if _, woken := event.Actions.StateDelta[stateWakeup]; woken {
		return AuthorApps
	}
	return AuthorMember
}

// responseError is the error a tool result reports, conventionally under
// "error". An empty error string reports no error, just like an absent one.
func responseError(response map[string]any) string {
	value, ok := response["error"]
	if !ok || value == nil {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		encoded, err := json.Marshal(value)
		if err != nil {
			return "the tool failed"
		}
		text = string(encoded)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if utf8.RuneCountInString(text) > maxErrorRunes {
		text = string([]rune(text)[:maxErrorRunes]) + "…"
	}
	return text
}

// boundedArguments is a tool call's arguments, or nothing when they are too
// large to show.
func boundedArguments(arguments map[string]any) map[string]any {
	if len(arguments) == 0 {
		return nil
	}
	encoded, err := json.Marshal(arguments)
	if err != nil || len(encoded) > maxArgumentBytes {
		return nil
	}
	return arguments
}

// decodeInto converts value, a struct or its decoded JSON form, into target.
func decodeInto(value any, target any) bool {
	if value == nil {
		return false
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	return json.Unmarshal(encoded, target) == nil
}
