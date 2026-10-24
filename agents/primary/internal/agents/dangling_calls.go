package agents

import (
	"context"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool/toolconfirmation"
)

// interruptedResult is the result given to a function call that nobody
// answered. Whether its tool ran, and what it did, is not known.
const interruptedResult = "This call was interrupted before it returned, so there is no result. It may or may not have taken effect; check before repeating it."

// InterruptedCallsInvocation is the invocation ID of every event
// InterruptedCallEvents makes. It names no run of the agent's.
const InterruptedCallsInvocation = "interrupted-calls"

// waitingCallNames are the calls ADK makes to wait on the member: a tool's
// confirmation, and a credential it needs. They are answered by the member,
// never as interrupted.
var waitingCallNames = map[string]bool{
	toolconfirmation.FunctionCallName: true,
	"adk_request_credential":          true,
}

// InterruptedCallEvents returns the events that answer, as interrupted, every
// function call in a thread's events that nothing answers: one event for
// each event that made such calls, holding a response to each of them, by
// the same author, on the same branch and in the same isolation scope, so
// ADK reads it as that agent's tool results.
//
// A run cut off after a model turn was stored and before its tools answered,
// as a deploy cuts one, leaves such calls behind. Since ADK v2.5, ADK drops
// a call with no response from what it sends the model, so the model would
// never learn the call was made, and could make it again: a send the member
// confirmed, sent twice. Stored in the thread before the next run, these
// responses tell it the call was interrupted and to check before repeating
// it, once, for every later run alike.
//
// A call is left alone when anything in events answers it, when it has no ID
// to answer it by (answerDanglingCalls answers that one in the request),
// when it is waiting on the member — a confirmation or credential request,
// or any call ADK marked long-running — or when a compaction already covers
// it.
func InterruptedCallEvents(ctx context.Context, events []*session.Event) []*session.Event {
	answered := map[string]bool{}
	var compactedThrough time.Time
	for _, event := range events {
		if event == nil {
			continue
		}
		for _, id := range event.LongRunningToolIDs {
			answered[id] = true
		}
		if event.Content != nil {
			for _, part := range event.Content.Parts {
				if part != nil && part.FunctionResponse != nil && part.FunctionResponse.ID != "" {
					answered[part.FunctionResponse.ID] = true
				}
			}
		}
		if compaction := event.Actions.Compaction; compaction != nil && compaction.EndTimestamp.After(compactedThrough) {
			compactedThrough = compaction.EndTimestamp
		}
	}
	var repairs []*session.Event
	for _, event := range events {
		if event == nil || event.Content == nil || event.Partial || !event.Timestamp.After(compactedThrough) {
			continue
		}
		var responses []*genai.Part
		for _, part := range event.Content.Parts {
			if part == nil || part.FunctionCall == nil {
				continue
			}
			call := part.FunctionCall
			if call.ID == "" || answered[call.ID] || waitingCallNames[call.Name] {
				continue
			}
			responses = append(responses, &genai.Part{FunctionResponse: &genai.FunctionResponse{
				ID:       call.ID,
				Name:     call.Name,
				Response: map[string]any{"error": interruptedResult},
			}})
		}
		if len(responses) == 0 {
			continue
		}
		repair := session.NewEvent(ctx, InterruptedCallsInvocation)
		repair.Author = event.Author
		repair.Branch = event.Branch
		repair.IsolationScope = event.IsolationScope
		repair.Content = &genai.Content{Role: genai.RoleUser, Parts: responses}
		repairs = append(repairs, repair)
	}
	return repairs
}

// answerDanglingCalls returns contents with a function response for every
// function call that has none.
//
// It is the fallback to InterruptedCallEvents, which answers such calls in
// the thread itself: it still answers a call with no ID, which ADK keeps,
// and any call reaching a model some other way.
//
// A run cancelled after a model turn was stored and before its tools
// answered, as a deploy cancels one, leaves the thread's history ending in
// calls nobody answered. ADK drops such a call when it has an ID, and sends
// one without an ID as it is. Vertex refuses a
// function call turn unless the turn after it holds a response for each
// call, so it would refuse every later request on the thread the same way.
// The missing responses say the call was interrupted. They go in a turn of
// their own after the calls when the turn after has no responses at all, and
// among its responses when it has some.
//
// A response answers the call with its id where there is one, otherwise the
// first call of its name that has no response yet. What it changes it
// copies, since a request's contents can be the session's own events.
func answerDanglingCalls(contents []*genai.Content) []*genai.Content {
	var added, completed map[int]*genai.Content
	for index, content := range contents {
		if content == nil {
			continue
		}
		calls := callParts(content.Parts)
		if len(calls) == 0 {
			continue
		}
		var following *genai.Content
		if index+1 < len(contents) {
			following = contents[index+1]
		}
		var followingParts []*genai.Part
		if following != nil {
			followingParts = following.Parts
		}
		responses := responseParts(followingParts)
		// Vertex counts the responses against the calls, so a turn with as
		// many as there are calls is left alone, whatever they are for.
		if len(responses) >= len(calls) {
			continue
		}
		if len(responses) == 0 {
			if added == nil {
				added = map[int]*genai.Content{}
			}
			added[index] = &genai.Content{Role: genai.RoleUser, Parts: answerAll(calls, nil)}
			continue
		}
		if completed == nil {
			completed = map[int]*genai.Content{}
		}
		completed[index+1] = &genai.Content{Role: following.Role, Parts: answerAll(calls, followingParts)}
	}
	if len(added) == 0 && len(completed) == 0 {
		return contents
	}
	repaired := make([]*genai.Content, 0, len(contents)+len(added))
	for index, content := range contents {
		if completion, ok := completed[index]; ok {
			content = completion
		}
		repaired = append(repaired, content)
		if turn, ok := added[index]; ok {
			repaired = append(repaired, turn)
		}
	}
	return repaired
}

// answerAll returns a function response for each of calls, in the order of
// the calls: the one among parts that answers it, or an interrupted one when
// there is none. The rest of parts follows.
func answerAll(calls, parts []*genai.Part) []*genai.Part {
	responses := responseParts(parts)
	answers := make([]*genai.Part, len(calls))
	taken := map[*genai.Part]bool{}
	// By id where a call has one, then by name in order. A call and a response
	// that both have an id, and not the same one, are for different calls.
	for _, byID := range []bool{true, false} {
		for i, call := range calls {
			if answers[i] != nil || (byID && call.FunctionCall.ID == "") {
				continue
			}
			for _, response := range responses {
				if taken[response] {
					continue
				}
				if byID && response.FunctionResponse.ID != call.FunctionCall.ID {
					continue
				}
				if !byID && (response.FunctionResponse.Name != call.FunctionCall.Name ||
					(call.FunctionCall.ID != "" && response.FunctionResponse.ID != "")) {
					continue
				}
				answers[i], taken[response] = response, true
				break
			}
		}
	}
	all := make([]*genai.Part, 0, len(calls)+len(parts))
	for i, call := range calls {
		if answers[i] == nil {
			answers[i] = &genai.Part{FunctionResponse: &genai.FunctionResponse{
				ID:       call.FunctionCall.ID,
				Name:     call.FunctionCall.Name,
				Response: map[string]any{"error": interruptedResult},
			}}
		}
		all = append(all, answers[i])
	}
	for _, part := range parts {
		if part != nil && !taken[part] {
			all = append(all, part)
		}
	}
	return all
}

// callParts returns the parts that are function calls.
func callParts(parts []*genai.Part) []*genai.Part {
	var calls []*genai.Part
	for _, part := range parts {
		if part != nil && part.FunctionCall != nil {
			calls = append(calls, part)
		}
	}
	return calls
}

// responseParts returns the parts that are function responses.
func responseParts(parts []*genai.Part) []*genai.Part {
	var responses []*genai.Part
	for _, part := range parts {
		if part != nil && part.FunctionResponse != nil {
			responses = append(responses, part)
		}
	}
	return responses
}
